// Package conformance is a reusable test suite that exercises any Hypervisor
// implementation through its full lifecycle. Implementations call Run from
// their own _test.go file, passing a constructor that wires the hypervisor up
// against the implementation's preferred test backend (mock in-memory, govmomi
// simulator, libvirt test driver, real KVM box, etc.).
//
// Adding a new operation to the Hypervisor interface? Add the assertion here
// and every backend's tests fail until they implement it. That's the point.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// Factory builds a fresh Hypervisor instance for one test, plus the
// backend-specific defaults that the conformance suite needs. The factory
// should register any teardown via t.Cleanup (not return a cleanup func),
// so the suite can register its own t.Cleanup for VM destroy AFTER the
// factory's cleanup — LIFO ordering then destroys VMs before closing the
// connection.
type Factory func(t *testing.T) (h hypervisor.Hypervisor, defaults Defaults)

// Defaults are backend-specific knobs the conformance suite needs to be
// portable across vSphere, real KVM, and the in-memory mock.
type Defaults struct {
	// ISOPath is a path to a file that exists on the backend and can be
	// attached as a CDROM. For the mock and govmomi simulator, any string
	// is fine. For real KVM, the file must exist or PowerOn will fail with
	// "No such file or directory."
	ISOPath string
}

// SampleSpec returns a VMSpec shaped like a nested ESXi host built with the
// proven defaults: SATA boot disk presented as SSD, vmxnet3 NIC. Tests can
// override fields before calling CreateVM.
func SampleSpec(name string) spec.VMSpec {
	return spec.VMSpec{
		Name:      name,
		VCPUs:     8,
		MemoryMiB: 32 * 1024,
		Firmware:  spec.FirmwareEFI,
		NestedHV:  true,
		GuestID:   "vmkernel7guest",
		Disks: []spec.DiskSpec{
			{SizeGiB: 32, Bus: spec.DiskBusSATA, Thin: true}, // boot, SSD
		},
		NICs: []spec.NICSpec{
			// "default" works on real libvirt (the always-present default
			// network) and falls back to the first available network on
			// vSphere (the simulator + real vCenter both have such a
			// fallback). Keeps the conformance suite backend-portable.
			{NetworkRef: "default", Model: spec.NICVMXNET3},
		},
	}
}

// Run executes the full conformance suite. Each subtest is independent and
// uses a fresh hypervisor from the factory.
//
// Subtests register a t.Cleanup destroy as soon as a VM is created, so a
// mid-test failure still tears down the VM. Important for backends with
// persistent state (real libvirt, real vCenter) — without this, a leftover
// domain from a failed run gets returned by the next run's idempotent
// CreateVM and corrupts subsequent assertions.
func Run(t *testing.T, newH Factory) {
	t.Helper()

	// destroyOnExit registers a best-effort cleanup. Errors during cleanup
	// are logged but don't fail the test (the test has already failed if
	// cleanup is needed; cleanup errors are noise on top).
	destroyOnExit := func(t *testing.T, h hypervisor.Hypervisor, ref hypervisor.VMRef) {
		t.Helper()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.Destroy(ctx, ref); err != nil {
				t.Logf("cleanup: Destroy(%s): %v", ref.Name(), err)
			}
		})
	}

	t.Run("CreateVM_then_Lookup_returns_handle", func(t *testing.T) {
		h, _ := newH(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		name := uniqueName(t, "lookup")
		ref, err := h.CreateVM(ctx, SampleSpec(name))
		require.NoError(t, err)
		destroyOnExit(t, h, ref)
		require.NotNil(t, ref)
		assert.Equal(t, name, ref.Name())

		found, err := h.Lookup(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, name, found.Name())
	})

	t.Run("Lookup_missing_returns_ErrNotFound", func(t *testing.T) {
		h, _ := newH(t)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := h.Lookup(ctx, uniqueName(t, "missing"))
		require.Error(t, err)
		assert.True(t, hypervisor.IsNotFound(err), "expected IsNotFound, got %T: %v", err, err)
	})

	t.Run("CreateVM_is_idempotent_on_duplicate_name", func(t *testing.T) {
		h, _ := newH(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		name := uniqueName(t, "idempotent")
		ref1, err := h.CreateVM(ctx, SampleSpec(name))
		require.NoError(t, err)
		destroyOnExit(t, h, ref1)

		ref2, err := h.CreateVM(ctx, SampleSpec(name))
		require.NoError(t, err)
		assert.Equal(t, ref1.Name(), ref2.Name(),
			"second CreateVM with same name must return existing VM, not error")
	})

	t.Run("AttachDisk_AttachNIC_AttachISO_PowerOn_lifecycle", func(t *testing.T) {
		h, defs := newH(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		name := uniqueName(t, "lifecycle")
		ref, err := h.CreateVM(ctx, SampleSpec(name))
		require.NoError(t, err)
		destroyOnExit(t, h, ref)

		// vSAN cache disk
		require.NoError(t, h.AttachDisk(ctx, ref, spec.DiskSpec{
			SizeGiB: 90, Bus: spec.DiskBusSATA, Thin: true,
		}))

		// Extra NIC
		mac, err := h.AttachNIC(ctx, ref, spec.NICSpec{
			NetworkRef: "default", Model: spec.NICVMXNET3,
		})
		require.NoError(t, err)
		assert.NotEmpty(t, mac, "AttachNIC must return a non-empty assigned MAC")

		// AttachISO is optional — backends without a usable ISO file leave
		// defs.ISOPath empty. PowerOn still runs either way.
		if defs.ISOPath != "" {
			require.NoError(t, h.AttachISO(ctx, ref, defs.ISOPath))
		}
		require.NoError(t, h.PowerOn(ctx, ref))
	})

	t.Run("Destroy_is_idempotent", func(t *testing.T) {
		h, _ := newH(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		name := uniqueName(t, "destroy")
		ref, err := h.CreateVM(ctx, SampleSpec(name))
		require.NoError(t, err)
		// No destroyOnExit here — this test exercises Destroy directly and
		// asserts the second call is a no-op.
		require.NoError(t, h.Destroy(ctx, ref))
		require.NoError(t, h.Destroy(ctx, ref), "second Destroy on same ref must be a no-op")
	})
}

// uniqueName returns a short, deterministic VM name for a subtest. Format:
// "cd-<6-char-hash-of-test-name>-<suffix>". Stays under vSphere's 80-char
// VM name limit and is stable across runs (so cleanup is reproducible).
func uniqueName(t *testing.T, suffix string) string {
	sum := sha256.Sum256([]byte(t.Name()))
	return "cd-" + hex.EncodeToString(sum[:3]) + "-" + suffix
}
