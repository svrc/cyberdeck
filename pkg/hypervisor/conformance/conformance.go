// Package conformance is a reusable Ginkgo contract that exercises any
// Hypervisor implementation through its full lifecycle. Backends call
// HypervisorContract from inside their own Describe/Context, passing a
// factory that wires the hypervisor up against the backend's preferred test
// target (mock in-memory, govmomi simulator, real libvirtd, real vCenter,
// etc.).
//
// Adding a new operation to the Hypervisor interface? Add the It here and
// every backend's contract suite fails until they implement it. That's the
// point.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// Factory builds a fresh Hypervisor instance for one spec, plus the
// backend-specific defaults the contract needs. The returned cleanup func
// (may be nil) is invoked in AfterEach AFTER the contract's per-VM Destroys,
// so connection/socket teardown happens last.
type Factory func() (h hypervisor.Hypervisor, defaults Defaults, cleanup func())

// Defaults are backend-specific knobs the contract needs to be portable
// across the mock, govmomi simulator, real KVM, and real vCenter.
type Defaults struct {
	// ISOPath is a path to a file that exists on the backend and can be
	// attached as a CDROM. For mocks and the govmomi simulator, any string
	// is fine. For real KVM/vCenter the file must exist or PowerOn fails
	// with "No such file or directory."
	ISOPath string
}

// SampleSpec returns a VMSpec shaped like a nested ESXi host built with the
// proven defaults: SATA boot disk presented as SSD, vmxnet3 NIC. Specs can
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
			{SizeGiB: 32, Bus: spec.DiskBusSATA, Thin: true},
		},
		NICs: []spec.NICSpec{
			// "default" works on real libvirt (the always-present default
			// network) and falls back to "first available" on vSphere
			// (the simulator + real vCenter both have such a fallback).
			{NetworkRef: "default", Model: spec.NICVMXNET3},
		},
	}
}

// HypervisorContract registers the Hypervisor contract specs inside the
// current Ginkgo container. Each spec runs against a fresh factory-built
// hypervisor; per-spec VMs are destroyed in AfterEach in LIFO order before
// the backend's cleanup func runs.
func HypervisorContract(factory Factory) {
	var (
		h         hypervisor.Hypervisor
		defs      Defaults
		closeFn   func()
		ctx       context.Context
		cancel    context.CancelFunc
		toDestroy []hypervisor.VMRef
	)

	BeforeEach(func() {
		// Set ctx/cancel and reset state BEFORE calling the factory: the
		// factory is allowed to Skip() (real-infra contexts do this when
		// env vars are missing), and AfterEach still runs after a Skip.
		// Without this ordering, AfterEach would call cancel() on a nil
		// CancelFunc.
		ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		toDestroy = nil
		h, defs, closeFn = nil, Defaults{}, nil
		h, defs, closeFn = factory()
	})

	AfterEach(func() {
		// LIFO: per-spec VMs first, backend cleanup last. Errors here are
		// noise on top of whatever already failed — log and move on.
		if h != nil {
			for i := len(toDestroy) - 1; i >= 0; i-- {
				dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := h.Destroy(dctx, toDestroy[i]); err != nil {
					GinkgoWriter.Printf("cleanup: Destroy(%s): %v\n", toDestroy[i].Name(), err)
				}
				dcancel()
			}
		}
		if closeFn != nil {
			closeFn()
		}
		if cancel != nil {
			cancel()
		}
	})

	// track records a VM for AfterEach teardown. Call immediately after a
	// successful CreateVM so mid-spec failures still tear the VM down.
	track := func(ref hypervisor.VMRef) {
		toDestroy = append(toDestroy, ref)
	}

	Describe("VM identity", func() {
		It("returns a handle from CreateVM that Lookup can resolve", func() {
			name := uniqueName("lookup")
			ref, err := h.CreateVM(ctx, SampleSpec(name))
			Expect(err).ToNot(HaveOccurred())
			track(ref)
			Expect(ref).ToNot(BeNil())
			Expect(ref.Name()).To(Equal(name))

			found, err := h.Lookup(ctx, name)
			Expect(err).ToNot(HaveOccurred())
			Expect(found.Name()).To(Equal(name))
		})

		It("returns IsNotFound for an unknown VM name", func() {
			_, err := h.Lookup(ctx, uniqueName("missing"))
			Expect(err).To(HaveOccurred())
			Expect(hypervisor.IsNotFound(err)).To(BeTrue(),
				"expected IsNotFound, got %T: %v", err, err)
		})
	})

	Describe("CreateVM idempotency", func() {
		It("returns the existing VM when called twice with the same name", func() {
			name := uniqueName("idempotent")
			ref1, err := h.CreateVM(ctx, SampleSpec(name))
			Expect(err).ToNot(HaveOccurred())
			track(ref1)

			ref2, err := h.CreateVM(ctx, SampleSpec(name))
			Expect(err).ToNot(HaveOccurred())
			Expect(ref2.Name()).To(Equal(ref1.Name()),
				"second CreateVM with same name must return existing VM, not error")
		})
	})

	Describe("Full attach-and-power-on lifecycle", func() {
		It("attaches a disk and a NIC, attaches the ISO, and powers on", func() {
			name := uniqueName("lifecycle")
			ref, err := h.CreateVM(ctx, SampleSpec(name))
			Expect(err).ToNot(HaveOccurred())
			track(ref)

			By("attaching a vSAN cache disk")
			Expect(h.AttachDisk(ctx, ref, spec.DiskSpec{
				SizeGiB: 90, Bus: spec.DiskBusSATA, Thin: true,
			})).To(Succeed())

			By("attaching a vmotion NIC and receiving an assigned MAC")
			mac, err := h.AttachNIC(ctx, ref, spec.NICSpec{
				NetworkRef: "default", Model: spec.NICVMXNET3,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(mac).ToNot(BeEmpty(), "AttachNIC must return a non-empty assigned MAC")

			if defs.ISOPath != "" {
				By("attaching the install ISO")
				Expect(h.AttachISO(ctx, ref, defs.ISOPath)).To(Succeed())
			}

			By("powering the VM on")
			Expect(h.PowerOn(ctx, ref)).To(Succeed())
		})
	})

	Describe("Destroy idempotency", func() {
		It("treats a second Destroy on the same ref as a no-op", func() {
			name := uniqueName("destroy")
			ref, err := h.CreateVM(ctx, SampleSpec(name))
			Expect(err).ToNot(HaveOccurred())
			// No track() — this spec exercises Destroy directly.
			Expect(h.Destroy(ctx, ref)).To(Succeed())
			Expect(h.Destroy(ctx, ref)).To(Succeed(), "second Destroy must be a no-op")
		})
	})
}

// uniqueName returns a short, deterministic VM name for the current spec.
// Format: "cd-<6-char-sha256-of-spec-text>-<suffix>". Stays under vSphere's
// 80-char VM name limit; stable across runs so cleanup of leftover VMs from
// prior failed runs is reproducible.
func uniqueName(suffix string) string {
	sum := sha256.Sum256([]byte(CurrentSpecReport().FullText()))
	return "cd-" + hex.EncodeToString(sum[:3]) + "-" + suffix
}
