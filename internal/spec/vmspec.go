// Package spec defines hypervisor-agnostic VM shapes.
//
// A VMSpec describes "what we want" — the Hypervisor implementations translate
// it into backend-specific calls (govmomi for vSphere, libvirt domain XML for
// KVM, etc.). The fields here are deliberately the union of what nested ESXi
// needs, not a generic VM model.
package spec

type Firmware string

const (
	FirmwareEFI  Firmware = "efi"
	FirmwareBIOS Firmware = "bios"
)

type DiskBus string

const (
	// DiskBusSATA is the operational default for nested ESXi on KVM: virtio
	// isn't supported by ESXi, SCSI works but is slower, NVMe is finicky on
	// the simulator. SATA + rotation_rate=1 (SSD hint) is the proven combo.
	DiskBusSATA  DiskBus = "sata"
	DiskBusSCSI  DiskBus = "scsi"
	DiskBusNVMe  DiskBus = "nvme"
	DiskBusVirtio DiskBus = "virtio"
)

type NICModel string

const (
	// NICVMXNET3 is the default for nested ESXi: ESXi auto-detects it on
	// both vSphere and KVM (qemu has emulated vmxnet3 hardware since ~2010).
	// KVM caveat: there's an old qemu bug where vmxnet3 MTU is capped at
	// 3000 bytes, so don't plan on jumbo frames inside nested ESXi.
	NICVMXNET3 NICModel = "vmxnet3"
	NICE1000e  NICModel = "e1000e"
	NICVirtio  NICModel = "virtio"
)

type DiskSpec struct {
	SizeGiB    int
	Bus        DiskBus
	Thin       bool
	BackingURI string // optional pre-existing disk to attach (qcow2 path / vSphere datastore path)

	// IsHDD presents the disk to the guest as a spinning rotational device.
	// Default (false) presents as SSD — for KVM that means rotation_rate=1
	// in the target XML; for vSphere that means setting virtualSSD=1 in
	// extraConfig per the legacy ESXiBuild.psm1 pattern.
	IsHDD bool
}

type NICSpec struct {
	// NetworkRef selects the backing network. Format depends on backend:
	//   - vSphere: portgroup name (e.g. "VM Network")
	//   - KVM:     "bridge:<name>" for a host Linux bridge (e.g. "bridge:br-trunk"),
	//              or "network:<name>" for a libvirt-managed network
	//              (e.g. "network:default"). Bare names default to network:.
	NetworkRef string
	Model      NICModel
	MAC        string // optional; if empty, hypervisor assigns
}

// VMSpec is the request shape passed to Hypervisor.CreateVM.
type VMSpec struct {
	Name           string
	VCPUs          int
	MemoryMiB      int
	Firmware       Firmware
	NestedHV       bool
	GuestID        string // vSphere guestId; e.g. "vmkernel7guest" for nested ESXi
	Disks          []DiskSpec
	NICs           []NICSpec
	ISOPath        string // optional: attach as CDROM at create time
	PlacementHints PlacementHints
}

// PlacementHints carries backend-specific placement preferences. Each
// implementation reads only the fields it understands.
type PlacementHints struct {
	// vSphere
	Datacenter   string
	Cluster      string
	ResourcePool string
	Datastore    string
	Folder       string

	// KVM
	StoragePool string // libvirt storage pool name for new disks
}
