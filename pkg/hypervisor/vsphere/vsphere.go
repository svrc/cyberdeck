// Package vsphere is a Hypervisor implementation backed by govmomi.
//
// One implementation handles both vCenter and ESXi-direct modes — they speak
// the same SOAP API, with vCenter exposing extra placement primitives
// (Datacenter / Cluster / ResourcePool / Folder) that ESXi-direct doesn't.
// PlacementHints fields are read defensively so the same code works on both.
//
// The minimum viable surface for the spike: CreateVM with disks/NICs/CDROM,
// Lookup, Attach* (each as a Reconfigure), PowerOn, Destroy. No advanced VM
// features (vSAN ESA NVMe spec, NUMA tuning, advanced settings) — those layer
// on cleanly via additional spec fields and Reconfigure calls in subsequent
// iterations.
package vsphere

import (
	"context"
	"errors"
	"fmt"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// Hypervisor is the govmomi-backed Hypervisor.
type Hypervisor struct {
	client *govmomi.Client
	finder *find.Finder

	// Resolved at New time from PlacementHints + sane defaults. The simulator
	// and a tiny vCenter both expose a single DC / cluster / datastore, which
	// is what these defaults handle.
	datacenter   *object.Datacenter
	resourcePool *object.ResourcePool
	datastore    *object.Datastore
	folder       *object.Folder
	network      object.NetworkReference
}

// Config is what New needs. The Client must be already-connected; New does
// not own its lifecycle (so the same client can be shared across hypervisor
// instances, useful for tests).
type Config struct {
	Client       *govmomi.Client
	Datacenter   string // optional; "" -> default
	Cluster      string // optional; "" -> default cluster's resource pool
	ResourcePool string // optional; if set, takes precedence over Cluster
	Datastore    string // optional; "" -> first datastore
	Folder       string // optional; "" -> default VM folder
	Network      string // optional; "" -> first network in the DC
}

// New wires a Hypervisor onto an existing govmomi client. Resolves placement
// references up front so Create/Attach calls don't pay the find cost.
func New(ctx context.Context, cfg Config) (*Hypervisor, error) {
	if cfg.Client == nil {
		return nil, errors.New("vsphere: Client is required")
	}
	finder := find.NewFinder(cfg.Client.Client, true)

	dc, err := finder.DatacenterOrDefault(ctx, cfg.Datacenter)
	if err != nil {
		return nil, fmt.Errorf("vsphere: resolve datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	pool, err := resolvePool(ctx, finder, cfg)
	if err != nil {
		return nil, fmt.Errorf("vsphere: resolve resource pool: %w", err)
	}

	ds, err := resolveDatastore(ctx, finder, cfg.Datastore)
	if err != nil {
		return nil, fmt.Errorf("vsphere: resolve datastore: %w", err)
	}

	folder, err := finder.FolderOrDefault(ctx, cfg.Folder)
	if err != nil {
		return nil, fmt.Errorf("vsphere: resolve folder: %w", err)
	}

	netRef, err := resolveNetwork(ctx, finder, cfg.Network)
	if err != nil {
		return nil, fmt.Errorf("vsphere: resolve network: %w", err)
	}

	return &Hypervisor{
		client:       cfg.Client,
		finder:       finder,
		datacenter:   dc,
		resourcePool: pool,
		datastore:    ds,
		folder:       folder,
		network:      netRef,
	}, nil
}

func (h *Hypervisor) Close() error { return nil } // client lifecycle owned by caller

// --- ref ---

type vmRef struct {
	id   string
	name string
}

func (r vmRef) ID() string   { return r.id }
func (r vmRef) Name() string { return r.name }

func refOf(vm *object.VirtualMachine) vmRef {
	return vmRef{id: vm.Reference().Value, name: vm.Name()}
}

// --- Hypervisor methods ---

func (h *Hypervisor) CreateVM(ctx context.Context, vm spec.VMSpec) (hypervisor.VMRef, error) {
	if existing, err := h.lookupVM(ctx, vm.Name); err == nil {
		return refOf(existing), nil // idempotent
	}

	dsName := h.datastore.Name()
	configSpec := types.VirtualMachineConfigSpec{
		Name:              vm.Name,
		GuestId:           vm.GuestID,
		NumCPUs:           int32(vm.VCPUs),
		MemoryMB:          int64(vm.MemoryMiB),
		NestedHVEnabled:   boolPtr(vm.NestedHV),
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", dsName),
		},
	}
	if vm.Firmware == spec.FirmwareEFI {
		configSpec.Firmware = string(types.GuestOsDescriptorFirmwareTypeEfi)
	}

	// Build initial device set: SCSI controller + each disk + each NIC +
	// optional CDROM. Doing this at create time is cheaper than a string of
	// post-create Reconfigures and matches what New-VM does in PowerCLI.
	deviceChange, err := h.initialDevices(vm)
	if err != nil {
		return nil, err
	}
	configSpec.DeviceChange = deviceChange

	task, err := h.folder.CreateVM(ctx, configSpec, h.resourcePool, nil)
	if err != nil {
		return nil, fmt.Errorf("vsphere: CreateVM submit: %w", err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("vsphere: CreateVM wait: %w", err)
	}
	moRef, ok := info.Result.(types.ManagedObjectReference)
	if !ok {
		return nil, fmt.Errorf("vsphere: CreateVM returned unexpected result type %T", info.Result)
	}

	return vmRef{id: moRef.Value, name: vm.Name}, nil
}

func (h *Hypervisor) Lookup(ctx context.Context, name string) (hypervisor.VMRef, error) {
	vm, err := h.lookupVM(ctx, name)
	if err != nil {
		return nil, err
	}
	return refOf(vm), nil
}

func (h *Hypervisor) AttachDisk(ctx context.Context, ref hypervisor.VMRef, disk spec.DiskSpec) error {
	vm, err := h.lookupVM(ctx, ref.Name())
	if err != nil {
		return err
	}
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: read devices: %w", err)
	}
	controller, err := pickDiskController(devices, disk.Bus)
	if err != nil {
		return err
	}
	dev := devices.CreateDisk(controller, h.datastore.Reference(), "")
	dev.CapacityInKB = int64(disk.SizeGiB) * 1024 * 1024
	if backing, ok := dev.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
		backing.ThinProvisioned = boolPtr(disk.Thin)
		backing.DiskMode = string(types.VirtualDiskModePersistent)
	}
	return reconfigure(ctx, vm, &types.VirtualDeviceConfigSpec{
		Operation:     types.VirtualDeviceConfigSpecOperationAdd,
		FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
		Device:        dev,
	})
}

func (h *Hypervisor) AttachNIC(ctx context.Context, ref hypervisor.VMRef, nic spec.NICSpec) (string, error) {
	vm, err := h.lookupVM(ctx, ref.Name())
	if err != nil {
		return "", err
	}
	dev, err := h.buildNICDevice(ctx, nic)
	if err != nil {
		return "", err
	}
	if err := reconfigure(ctx, vm, &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    dev,
	}); err != nil {
		return "", err
	}
	// Re-read to get the assigned MAC.
	devices, err := vm.Device(ctx)
	if err != nil {
		return "", fmt.Errorf("vsphere: re-read devices: %w", err)
	}
	for _, d := range devices {
		if card, ok := d.(types.BaseVirtualEthernetCard); ok {
			eth := card.GetVirtualEthernetCard()
			if eth.MacAddress != "" {
				// Best-effort: return the most-recent NIC's MAC. For the spike
				// we don't track a stable per-attach key.
				return eth.MacAddress, nil
			}
		}
	}
	return "", nil
}

func (h *Hypervisor) AttachISO(ctx context.Context, ref hypervisor.VMRef, isoPath string) error {
	vm, err := h.lookupVM(ctx, ref.Name())
	if err != nil {
		return err
	}
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: read devices: %w", err)
	}
	ide, err := devices.FindIDEController("")
	if err != nil {
		return fmt.Errorf("vsphere: find IDE controller: %w", err)
	}
	cdrom, err := devices.CreateCdrom(ide)
	if err != nil {
		return fmt.Errorf("vsphere: create cdrom: %w", err)
	}
	devices.InsertIso(cdrom, isoPath)
	return reconfigure(ctx, vm, &types.VirtualDeviceConfigSpec{
		Operation: types.VirtualDeviceConfigSpecOperationAdd,
		Device:    cdrom,
	})
}

func (h *Hypervisor) PowerOn(ctx context.Context, ref hypervisor.VMRef) error {
	vm, err := h.lookupVM(ctx, ref.Name())
	if err != nil {
		return err
	}
	task, err := vm.PowerOn(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: PowerOn submit: %w", err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return fmt.Errorf("vsphere: PowerOn wait: %w", err)
	}
	return nil
}

func (h *Hypervisor) Destroy(ctx context.Context, ref hypervisor.VMRef) error {
	vm, err := h.lookupVM(ctx, ref.Name())
	if err != nil {
		if hypervisor.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Best-effort power-off; ignore "already off" errors.
	var moVM mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"runtime.powerState"}, &moVM); err == nil {
		if moVM.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
			if task, err := vm.PowerOff(ctx); err == nil {
				_, _ = task.WaitForResult(ctx, nil)
			}
		}
	}

	task, err := vm.Destroy(ctx)
	if err != nil {
		return fmt.Errorf("vsphere: Destroy submit: %w", err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return fmt.Errorf("vsphere: Destroy wait: %w", err)
	}
	return nil
}

// --- internals ---

func (h *Hypervisor) lookupVM(ctx context.Context, name string) (*object.VirtualMachine, error) {
	vm, err := h.finder.VirtualMachine(ctx, name)
	if err != nil {
		var notFound *find.NotFoundError
		if errors.As(err, &notFound) {
			return nil, hypervisor.ErrNotFound(name)
		}
		return nil, fmt.Errorf("vsphere: Lookup: %w", err)
	}
	return vm, nil
}

func (h *Hypervisor) initialDevices(vm spec.VMSpec) ([]types.BaseVirtualDeviceConfigSpec, error) {
	var changes []types.BaseVirtualDeviceConfigSpec

	// Pre-declare both a SCSI (key=-1) and a SATA/AHCI (key=-2) controller.
	// Cheap to have both, and ESXi-on-vSphere benefits from SATA being
	// present for the install CDROM and for SSD-presented disks (rotation
	// rate / virtualSSD semantics carry over).
	scsi := &types.ParaVirtualSCSIController{
		VirtualSCSIController: types.VirtualSCSIController{
			SharedBus: types.VirtualSCSISharingNoSharing,
			VirtualController: types.VirtualController{
				BusNumber: 0,
				VirtualDevice: types.VirtualDevice{Key: -1},
			},
		},
	}
	sata := &types.VirtualAHCIController{
		VirtualSATAController: types.VirtualSATAController{
			VirtualController: types.VirtualController{
				BusNumber: 0,
				VirtualDevice: types.VirtualDevice{Key: -2},
			},
		},
	}
	changes = append(changes,
		&types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    scsi,
		},
		&types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    sata,
		},
	)

	// Disks at create time. Attach SCSI disks to controller -1, SATA disks
	// to controller -2. Per-bus unit-number counters keep slots distinct.
	dsName := h.datastore.Name()
	scsiSlot, sataSlot := 0, 0
	for i, d := range vm.Disks {
		ctrlKey := int32(-1)
		switch normalizedBus(d.Bus) {
		case spec.DiskBusSATA:
			ctrlKey = -2
		case spec.DiskBusNVMe:
			// NVMe controller would need its own pre-declared key; defer
			// to post-create AttachDisk for now.
			continue
		}
		var unit int32
		if ctrlKey == -2 {
			unit = int32(sataSlot)
			sataSlot++
		} else {
			unit = int32(scsiSlot)
			scsiSlot++
		}
		disk := &types.VirtualDisk{
			CapacityInKB: int64(d.SizeGiB) * 1024 * 1024,
			VirtualDevice: types.VirtualDevice{
				Key:           int32(-10 - i),
				ControllerKey: ctrlKey,
				UnitNumber:    int32Ptr(unit),
				Backing: &types.VirtualDiskFlatVer2BackingInfo{
					DiskMode:        string(types.VirtualDiskModePersistent),
					ThinProvisioned: boolPtr(d.Thin),
					VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
						FileName: fmt.Sprintf("[%s]", dsName),
					},
				},
			},
		}
		changes = append(changes, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
			Device:        disk,
		})
	}

	// NICs at create time.
	for _, n := range vm.NICs {
		nic, err := h.buildNICDeviceForNetwork(n, h.network)
		if err != nil {
			return nil, err
		}
		changes = append(changes, &types.VirtualDeviceConfigSpec{
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
			Device:    nic,
		})
	}

	return changes, nil
}

func (h *Hypervisor) buildNICDevice(ctx context.Context, n spec.NICSpec) (types.BaseVirtualDevice, error) {
	netRef := h.network
	if n.NetworkRef != "" {
		nr, err := h.finder.Network(ctx, n.NetworkRef)
		if err == nil {
			netRef = nr
		}
		// If named network not found, fall back to default (matches simulator
		// where the default network is "VM Network" but custom names may not
		// resolve).
	}
	return h.buildNICDeviceForNetwork(n, netRef)
}

func (h *Hypervisor) buildNICDeviceForNetwork(n spec.NICSpec, netRef object.NetworkReference) (types.BaseVirtualDevice, error) {
	backing, err := netRef.EthernetCardBackingInfo(context.Background())
	if err != nil {
		return nil, fmt.Errorf("vsphere: NIC backing: %w", err)
	}
	model := "e1000e"
	switch n.Model {
	case spec.NICVMXNET3:
		model = "vmxnet3"
	case spec.NICVirtio:
		// vSphere doesn't have virtio; downgrade to vmxnet3 silently. Real
		// implementations might want to error instead.
		model = "vmxnet3"
	}
	dev, err := object.EthernetCardTypes().CreateEthernetCard(model, backing)
	if err != nil {
		return nil, fmt.Errorf("vsphere: create NIC %s: %w", model, err)
	}
	if n.MAC != "" {
		if card, ok := dev.(types.BaseVirtualEthernetCard); ok {
			eth := card.GetVirtualEthernetCard()
			eth.AddressType = string(types.VirtualEthernetCardMacTypeManual)
			eth.MacAddress = n.MAC
		}
	}
	return dev, nil
}

func pickDiskController(devices object.VirtualDeviceList, bus spec.DiskBus) (types.BaseVirtualController, error) {
	switch normalizedBus(bus) {
	case spec.DiskBusSATA:
		c, err := devices.FindDiskController("sata")
		if err != nil {
			return nil, fmt.Errorf("vsphere: find SATA controller: %w", err)
		}
		return c, nil
	case spec.DiskBusSCSI:
		c, err := devices.FindDiskController("scsi")
		if err != nil {
			return nil, fmt.Errorf("vsphere: find SCSI controller: %w", err)
		}
		return c, nil
	case spec.DiskBusNVMe:
		c, err := devices.FindDiskController("nvme")
		if err != nil {
			return nil, fmt.Errorf("vsphere: find NVMe controller: %w", err)
		}
		return c, nil
	default:
		return nil, fmt.Errorf("vsphere: unsupported disk bus %q", bus)
	}
}

func reconfigure(ctx context.Context, vm *object.VirtualMachine, change types.BaseVirtualDeviceConfigSpec) error {
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{
		DeviceChange: []types.BaseVirtualDeviceConfigSpec{change},
	})
	if err != nil {
		return fmt.Errorf("vsphere: Reconfigure submit: %w", err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return fmt.Errorf("vsphere: Reconfigure wait: %w", err)
	}
	return nil
}

// resolvePool picks a resource pool by priority:
//  1. cfg.ResourcePool by exact path
//  2. cfg.Cluster's root pool
//  3. DefaultResourcePool — but if that's ambiguous (e.g. govmomi simulator's
//     VPX model with multiple clusters), fall back to the first cluster's pool.
func resolvePool(ctx context.Context, finder *find.Finder, cfg Config) (*object.ResourcePool, error) {
	if cfg.ResourcePool != "" {
		return finder.ResourcePool(ctx, cfg.ResourcePool)
	}
	if cfg.Cluster != "" {
		cluster, err := finder.ClusterComputeResource(ctx, cfg.Cluster)
		if err != nil {
			return nil, err
		}
		return cluster.ResourcePool(ctx)
	}
	pool, err := finder.DefaultResourcePool(ctx)
	if err == nil {
		return pool, nil
	}
	// Ambiguity: multiple clusters. Pick the first deterministically.
	clusters, listErr := finder.ClusterComputeResourceList(ctx, "*")
	if listErr != nil || len(clusters) == 0 {
		return nil, err // surface the original DefaultResourcePool error
	}
	return clusters[0].ResourcePool(ctx)
}

// resolveDatastore picks a datastore by name; if name is empty and the
// default is ambiguous (real vCenters routinely have multiple), falls back
// to the first datastore listed.
func resolveDatastore(ctx context.Context, finder *find.Finder, name string) (*object.Datastore, error) {
	if name != "" {
		return finder.Datastore(ctx, name)
	}
	ds, err := finder.DefaultDatastore(ctx)
	if err == nil {
		return ds, nil
	}
	all, listErr := finder.DatastoreList(ctx, "*")
	if listErr != nil || len(all) == 0 {
		return nil, err
	}
	return all[0], nil
}

// resolveNetwork picks a network by name; if name is empty and the default is
// ambiguous, falls back to the first network listed in the inventory. Same
// rationale as resolvePool — the simulator's VPX model has multiple networks.
func resolveNetwork(ctx context.Context, finder *find.Finder, name string) (object.NetworkReference, error) {
	if name != "" {
		return finder.Network(ctx, name)
	}
	netRef, err := finder.DefaultNetwork(ctx)
	if err == nil {
		return netRef, nil
	}
	nets, listErr := finder.NetworkList(ctx, "*")
	if listErr != nil || len(nets) == 0 {
		return nil, err
	}
	return nets[0], nil
}

func normalizedBus(b spec.DiskBus) spec.DiskBus {
	if b == "" {
		return spec.DiskBusSATA // operational default
	}
	return b
}

func boolPtr(b bool) *bool   { return &b }
func int32Ptr(i int32) *int32 { return &i }
