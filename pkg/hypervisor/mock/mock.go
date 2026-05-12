// Package mock is an in-memory Hypervisor implementation. It captures every
// call as state so tests can assert "the workflow asked the hypervisor for X
// disks of Y shape, with Z NICs," without standing up real infrastructure.
package mock

import (
	"context"
	"fmt"
	"sync"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// VM is the recorded state for a mock VM. Tests inspect this directly.
type VM struct {
	Spec     spec.VMSpec
	Disks    []spec.DiskSpec
	NICs     []NICAttachment
	ISOPaths []string
	PoweredOn bool
	Destroyed bool
}

type NICAttachment struct {
	Spec spec.NICSpec
	MAC  string
}

type ref struct {
	id   string
	name string
}

func (r ref) ID() string   { return r.id }
func (r ref) Name() string { return r.name }

// Hypervisor is the in-memory implementation.
type Hypervisor struct {
	mu      sync.Mutex
	vms     map[string]*VM
	nextID  int
	macSeed int
}

func New() *Hypervisor {
	return &Hypervisor{vms: make(map[string]*VM)}
}

// VMs returns a snapshot of recorded VMs (test helper).
func (h *Hypervisor) VMs() map[string]*VM {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]*VM, len(h.vms))
	for k, v := range h.vms {
		copy := *v
		out[k] = &copy
	}
	return out
}

// Get returns the recorded VM (test helper). Returns nil if absent.
func (h *Hypervisor) Get(name string) *VM {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v, ok := h.vms[name]; ok {
		copy := *v
		return &copy
	}
	return nil
}

func (h *Hypervisor) CreateVM(_ context.Context, vmSpec spec.VMSpec) (hypervisor.VMRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.vms[vmSpec.Name]; ok && !existing.Destroyed {
		// Idempotent: return handle without mutation.
		return ref{id: vmSpec.Name, name: vmSpec.Name}, nil
	}

	h.nextID++
	vm := &VM{
		Spec:     vmSpec,
		Disks:    append([]spec.DiskSpec(nil), vmSpec.Disks...),
		NICs:     make([]NICAttachment, 0, len(vmSpec.NICs)),
		ISOPaths: nil,
	}
	for _, n := range vmSpec.NICs {
		vm.NICs = append(vm.NICs, NICAttachment{Spec: n, MAC: h.assignMAC(n.MAC)})
	}
	if vmSpec.ISOPath != "" {
		vm.ISOPaths = append(vm.ISOPaths, vmSpec.ISOPath)
	}
	h.vms[vmSpec.Name] = vm
	return ref{id: vmSpec.Name, name: vmSpec.Name}, nil
}

func (h *Hypervisor) Lookup(_ context.Context, name string) (hypervisor.VMRef, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if vm, ok := h.vms[name]; ok && !vm.Destroyed {
		return ref{id: name, name: name}, nil
	}
	return nil, hypervisor.ErrNotFound(name)
}

func (h *Hypervisor) AttachDisk(_ context.Context, r hypervisor.VMRef, disk spec.DiskSpec) error {
	return h.mutate(r, func(vm *VM) error {
		vm.Disks = append(vm.Disks, disk)
		return nil
	})
}

func (h *Hypervisor) AttachNIC(_ context.Context, r hypervisor.VMRef, nic spec.NICSpec) (string, error) {
	var assigned string
	err := h.mutate(r, func(vm *VM) error {
		mac := h.assignMAC(nic.MAC)
		vm.NICs = append(vm.NICs, NICAttachment{Spec: nic, MAC: mac})
		assigned = mac
		return nil
	})
	return assigned, err
}

func (h *Hypervisor) AttachISO(_ context.Context, r hypervisor.VMRef, isoPath string) error {
	return h.mutate(r, func(vm *VM) error {
		vm.ISOPaths = append(vm.ISOPaths, isoPath)
		return nil
	})
}

func (h *Hypervisor) PowerOn(_ context.Context, r hypervisor.VMRef) error {
	return h.mutate(r, func(vm *VM) error {
		vm.PoweredOn = true
		return nil
	})
}

func (h *Hypervisor) Destroy(_ context.Context, r hypervisor.VMRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if vm, ok := h.vms[r.Name()]; ok {
		vm.Destroyed = true
		vm.PoweredOn = false
		delete(h.vms, r.Name())
	}
	return nil
}

func (h *Hypervisor) Close() error { return nil }

// --- internal helpers ---

func (h *Hypervisor) mutate(r hypervisor.VMRef, fn func(*VM) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	vm, ok := h.vms[r.Name()]
	if !ok || vm.Destroyed {
		return hypervisor.ErrNotFound(r.Name())
	}
	return fn(vm)
}

// assignMAC must be called under h.mu.
func (h *Hypervisor) assignMAC(requested string) string {
	if requested != "" {
		return requested
	}
	h.macSeed++
	// 00:50:56 is the VMware OUI; harmless for a mock and matches what real
	// vSphere assigns, which makes assertion-by-prefix easier in tests.
	return fmt.Sprintf("00:50:56:%02x:%02x:%02x",
		(h.macSeed>>16)&0xff, (h.macSeed>>8)&0xff, h.macSeed&0xff)
}
