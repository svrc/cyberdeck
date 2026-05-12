// Package hypervisor defines the interface that every backend (vSphere, KVM,
// future libvirt-on-OpenStack, etc.) implements.
//
// The contract is intentionally narrow and operates on opaque VMRef handles.
// The point is that everything above this layer — workflows, the network
// manager, the VCF Installer client — depends only on this interface, not on
// govmomi, libvirt, or any other backend SDK.
package hypervisor

import (
	"context"

	"github.com/svrc/cyberdeck/internal/spec"
)

// VMRef is an opaque, backend-specific handle for a VM. Callers should treat
// it as a token: pass it back to the same Hypervisor instance, don't compare
// across backends, don't introspect.
type VMRef interface {
	// ID returns a stable, human-readable identifier (vSphere managed object
	// reference, libvirt domain UUID, etc.). Used for logging and idempotency
	// keys, not for cross-backend equivalence.
	ID() string
	// Name returns the VM name as known to the backend.
	Name() string
}

// Hypervisor is the contract every backend implements. All operations are
// expected to be safe to retry: implementations should treat repeated calls
// with the same logical inputs as idempotent (look up by name before creating,
// check current state before mutating). This matches Temporal's expectation
// that activities can be retried freely.
type Hypervisor interface {
	// CreateVM provisions the VM shell with disks, NICs, and (optionally) an
	// ISO attached. The VM is created powered-off. If a VM with the same name
	// already exists, CreateVM returns a handle to it without modifying it —
	// the caller should reconcile shape via Attach* calls explicitly.
	CreateVM(ctx context.Context, vm spec.VMSpec) (VMRef, error)

	// Lookup returns a handle to an existing VM by name, or (nil, ErrNotFound)
	// if no such VM exists. Used by activities to check state before acting.
	Lookup(ctx context.Context, name string) (VMRef, error)

	AttachDisk(ctx context.Context, ref VMRef, disk spec.DiskSpec) error
	AttachNIC(ctx context.Context, ref VMRef, nic spec.NICSpec) (mac string, err error)
	AttachISO(ctx context.Context, ref VMRef, isoPath string) error

	PowerOn(ctx context.Context, ref VMRef) error

	// Destroy powers off (if running) and deletes the VM and its disks.
	// Idempotent: returns nil if the VM doesn't exist.
	Destroy(ctx context.Context, ref VMRef) error

	// Close releases any backend connections. Safe to call multiple times.
	Close() error
}

// ErrNotFound is returned by Lookup when the named VM does not exist.
type notFoundError struct{ name string }

func (e notFoundError) Error() string { return "vm not found: " + e.name }

func ErrNotFound(name string) error { return notFoundError{name: name} }

func IsNotFound(err error) bool {
	_, ok := err.(notFoundError)
	return ok
}
