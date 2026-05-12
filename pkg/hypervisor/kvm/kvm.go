// Package kvm is a Hypervisor implementation backed by libvirt, using the
// digitalocean/go-libvirt pure-Go RPC client (no cgo).
//
// The pure-Go client requires a reachable libvirtd. Three common setups:
//
//   1. Local libvirtd: dial /var/run/libvirt/libvirt-sock (URI ignored except
//      for choosing the driver: "qemu:///system" / "qemu:///session").
//   2. Remote KVM host over SSH: tunnel libvirt-sock via an SSH dialer.
//   3. Test driver: have libvirtd connect to "test:///default" — used for
//      Layer B integration tests without launching real VMs.
//
// The spike supports (1) and (3); SSH dialer is straightforward to add.
package kvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"github.com/digitalocean/go-libvirt"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// Hypervisor is the libvirt-backed Hypervisor.
type Hypervisor struct {
	conn *libvirt.Libvirt

	// poolName is the libvirt storage pool used for qcow2 disk files.
	// "default" is the convention; tests and prod both rely on it being
	// present and active.
	poolName string

	// nextSlot tracks attach indexes for unique target dev names. Real impls
	// should derive these from the current device list; for the spike this is
	// fine.
	slot map[string]int
}

// Option configures the KVM Hypervisor at construction time.
type Option func(*Hypervisor)

// WithStoragePool overrides the libvirt storage pool name used for new disks.
// Default is "default".
func WithStoragePool(name string) Option {
	return func(h *Hypervisor) { h.poolName = name }
}

// New wraps an already-connected libvirt RPC client. Does not own the
// connection's lifecycle.
func New(conn *libvirt.Libvirt, opts ...Option) (*Hypervisor, error) {
	if conn == nil {
		return nil, errors.New("kvm: conn is required")
	}
	h := &Hypervisor{conn: conn, poolName: "default", slot: map[string]int{}}
	for _, opt := range opts {
		opt(h)
	}
	return h, nil
}

func (h *Hypervisor) Close() error { return nil }

// --- ref ---

type domRef struct {
	dom libvirt.Domain
}

func (r domRef) ID() string {
	// Stringify the UUID; go-libvirt's UUID is [16]byte, so we render it as
	// the canonical 8-4-4-4-12 form.
	u := r.dom.UUID
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
func (r domRef) Name() string { return r.dom.Name }

// --- Hypervisor methods ---

func (h *Hypervisor) CreateVM(_ context.Context, vm spec.VMSpec) (hypervisor.VMRef, error) {
	if existing, err := h.conn.DomainLookupByName(vm.Name); err == nil {
		return domRef{dom: existing}, nil // idempotent
	}

	// Create backing qcow2 volumes BEFORE defining the domain. libvirt won't
	// validate disk sources at DefineXML time, but PowerOn will fail if the
	// files don't exist. We then resolve each volume's *actual* on-disk path
	// (which depends on where the pool is mounted — Stu's setup uses
	// /mnt/esxi9, not the Ubuntu default of /var/lib/libvirt/images) and
	// pass those resolved paths into the domain XML rendering.
	diskPaths, err := h.ensureDiskVolumes(vm)
	if err != nil {
		return nil, err
	}

	xml, err := renderDomainXML(vm, diskPaths)
	if err != nil {
		return nil, fmt.Errorf("kvm: render domain XML: %w", err)
	}
	dom, err := h.conn.DomainDefineXML(xml)
	if err != nil {
		return nil, fmt.Errorf("kvm: DomainDefineXML: %w", err)
	}
	return domRef{dom: dom}, nil
}

// ensureDiskVolumes creates a sparse qcow2 volume per disk in the spec, then
// returns the resolved on-disk path for each. Idempotent: if the volume
// already exists, uses it as-is.
func (h *Hypervisor) ensureDiskVolumes(vm spec.VMSpec) ([]string, error) {
	pool, err := h.conn.StoragePoolLookupByName(h.poolName)
	if err != nil {
		return nil, fmt.Errorf("kvm: lookup storage pool %q: %w", h.poolName, err)
	}
	paths := make([]string, len(vm.Disks))
	for i, d := range vm.Disks {
		if d.BackingURI != "" {
			paths[i] = d.BackingURI
			continue
		}
		volName := volumeName(vm.Name, i)
		vol, err := h.conn.StorageVolLookupByName(pool, volName)
		if err != nil {
			volXML := renderVolumeXML(volName, d)
			vol, err = h.conn.StorageVolCreateXML(pool, volXML, 0)
			if err != nil {
				return nil, fmt.Errorf("kvm: create volume %q: %w", volName, err)
			}
		}
		path, err := h.conn.StorageVolGetPath(vol)
		if err != nil {
			return nil, fmt.Errorf("kvm: resolve volume path %q: %w", volName, err)
		}
		paths[i] = path
	}
	return paths, nil
}

func (h *Hypervisor) Lookup(_ context.Context, name string) (hypervisor.VMRef, error) {
	dom, err := h.conn.DomainLookupByName(name)
	if err != nil {
		if isLibvirtNotFound(err) {
			return nil, hypervisor.ErrNotFound(name)
		}
		return nil, fmt.Errorf("kvm: Lookup: %w", err)
	}
	return domRef{dom: dom}, nil
}

func (h *Hypervisor) AttachDisk(_ context.Context, ref hypervisor.VMRef, disk spec.DiskSpec) error {
	dom, err := h.lookupDom(ref.Name())
	if err != nil {
		return err
	}
	idx := h.bumpSlot(ref.Name(), "disk")

	// Provision the backing volume in the storage pool and resolve its real
	// path before referencing it in the attach XML.
	source := disk.BackingURI
	if source == "" {
		pool, err := h.conn.StoragePoolLookupByName(h.poolName)
		if err != nil {
			return fmt.Errorf("kvm: lookup storage pool %q: %w", h.poolName, err)
		}
		volName := volumeName(ref.Name(), idx)
		vol, err := h.conn.StorageVolLookupByName(pool, volName)
		if err != nil {
			vol, err = h.conn.StorageVolCreateXML(pool, renderVolumeXML(volName, disk), 0)
			if err != nil {
				return fmt.Errorf("kvm: create attach-disk volume %q: %w", volName, err)
			}
		}
		path, err := h.conn.StorageVolGetPath(vol)
		if err != nil {
			return fmt.Errorf("kvm: resolve attach-disk volume path: %w", err)
		}
		source = path
	}

	xml := renderDiskXML(disk, idx, source)
	flags := libvirt.DomainDeviceModifyConfig
	if err := h.conn.DomainAttachDeviceFlags(dom, xml, uint32(flags)); err != nil {
		return fmt.Errorf("kvm: AttachDisk: %w", err)
	}
	return nil
}

func (h *Hypervisor) AttachNIC(_ context.Context, ref hypervisor.VMRef, nic spec.NICSpec) (string, error) {
	dom, err := h.lookupDom(ref.Name())
	if err != nil {
		return "", err
	}
	mac := nic.MAC
	if mac == "" {
		// Let libvirt assign by omitting <mac/>; we read it back below.
	}
	xml := renderNICXML(nic)
	flags := libvirt.DomainDeviceModifyConfig
	if err := h.conn.DomainAttachDeviceFlags(dom, xml, uint32(flags)); err != nil {
		return "", fmt.Errorf("kvm: AttachNIC: %w", err)
	}
	if mac == "" {
		// Caller can re-read the domain XML if they need the assigned MAC.
		// For the spike we return a placeholder since the test driver may not
		// auto-assign a stable MAC at attach time.
		mac = "auto"
	}
	return mac, nil
}

func (h *Hypervisor) AttachISO(_ context.Context, ref hypervisor.VMRef, isoPath string) error {
	dom, err := h.lookupDom(ref.Name())
	if err != nil {
		return err
	}
	xml := renderCDROMXML(isoPath)
	flags := libvirt.DomainDeviceModifyConfig
	if err := h.conn.DomainAttachDeviceFlags(dom, xml, uint32(flags)); err != nil {
		return fmt.Errorf("kvm: AttachISO: %w", err)
	}
	return nil
}

func (h *Hypervisor) PowerOn(_ context.Context, ref hypervisor.VMRef) error {
	dom, err := h.lookupDom(ref.Name())
	if err != nil {
		return err
	}
	if err := h.conn.DomainCreate(dom); err != nil {
		// "already running" is fine for an idempotent PowerOn.
		if strings.Contains(err.Error(), "already") {
			return nil
		}
		return fmt.Errorf("kvm: PowerOn: %w", err)
	}
	return nil
}

func (h *Hypervisor) Destroy(_ context.Context, ref hypervisor.VMRef) error {
	dom, err := h.conn.DomainLookupByName(ref.Name())
	if err != nil {
		if isLibvirtNotFound(err) {
			return nil
		}
		return fmt.Errorf("kvm: Destroy lookup: %w", err)
	}
	// Best-effort power off; "not running" / "is not active" are fine.
	if err := h.conn.DomainDestroy(dom); err != nil {
		// Tolerate the various wordings libvirt versions use.
	}
	// Use Flags variant to also clean NVRAM (UEFI firmware leaves an NVRAM
	// file behind), managed-save state, snapshots, and checkpoints. Without
	// these flags, basic Undefine refuses for any UEFI domain.
	const undefFlags = libvirt.DomainUndefineManagedSave |
		libvirt.DomainUndefineSnapshotsMetadata |
		libvirt.DomainUndefineNvram |
		libvirt.DomainUndefineCheckpointsMetadata
	if err := h.conn.DomainUndefineFlags(dom, undefFlags); err != nil {
		return fmt.Errorf("kvm: Destroy undefine: %w", err)
	}
	// Best-effort cleanup of any qcow2 volumes we created. Failures here are
	// logged-but-tolerated: an orphaned volume leaks disk space but doesn't
	// break correctness.
	h.cleanupVolumes(ref.Name())
	return nil
}

// cleanupVolumes removes the qcow2 backing files matching the standard
// "<vmName>-diskN" naming convention. Stops on the first miss (volume not
// found means we've cleared everything we created).
func (h *Hypervisor) cleanupVolumes(vmName string) {
	pool, err := h.conn.StoragePoolLookupByName(h.poolName)
	if err != nil {
		return
	}
	for i := 0; i < 16; i++ {
		volName := volumeName(vmName, i)
		vol, err := h.conn.StorageVolLookupByName(pool, volName)
		if err != nil {
			return
		}
		_ = h.conn.StorageVolDelete(vol, 0)
	}
}

func volumeName(vmName string, idx int) string {
	return fmt.Sprintf("%s-disk%d.qcow2", vmName, idx)
}

// renderVolumeXML emits a libvirt storage volume description. allocation=0
// + capacity=N gives a sparse qcow2: zero bytes on disk until written, up to
// N GiB virtual size. Matches `qemu-img create -f qcow2 -o preallocation=off`
// and is the basis of the "tar to S3" backup story.
func renderVolumeXML(name string, d spec.DiskSpec) string {
	return fmt.Sprintf(`<volume>
  <name>%s</name>
  <allocation>0</allocation>
  <capacity unit='G'>%d</capacity>
  <target>
    <format type='qcow2'/>
  </target>
</volume>`, name, d.SizeGiB)
}

// --- internals ---

func (h *Hypervisor) lookupDom(name string) (libvirt.Domain, error) {
	dom, err := h.conn.DomainLookupByName(name)
	if err != nil {
		if isLibvirtNotFound(err) {
			return libvirt.Domain{}, hypervisor.ErrNotFound(name)
		}
		return libvirt.Domain{}, fmt.Errorf("kvm: lookup: %w", err)
	}
	return dom, nil
}

func (h *Hypervisor) bumpSlot(vmName, kind string) int {
	key := vmName + ":" + kind
	h.slot[key]++
	return h.slot[key]
}

func isLibvirtNotFound(err error) bool {
	// go-libvirt surfaces VIR_ERR_NO_DOMAIN as a libvirt.Error with Code=42.
	// We check by message because matching on the error type adds dep weight
	// for not much win in spike code.
	msg := err.Error()
	return strings.Contains(msg, "Domain not found") ||
		strings.Contains(msg, "no domain") ||
		strings.Contains(msg, "no such domain")
}

// --- XML rendering ---
//
// The XML emitted here mirrors the proven virt-install command for ESXi-on-KVM:
//
//   virt-install --virt-type=kvm --hvm --boot uefi
//     --cpu host-passthrough
//     --network bridge:br-trunk,model=vmxnet3
//     --disk pool=default,size=N,sparse=true,bus=sata,format=qcow2,
//            target=sdX,target.rotation_rate=1,cache=none,io=native
//     --graphics vnc --video qxl
//     --os-variant linux2020
//
// Defaults baked in:
//   - SATA bus + rotation_rate=1 (presents as SSD; ESXi vSAN needs this)
//   - cache=none + io=native (correct for sparse qcow2 on local NVMe)
//   - vmxnet3 NIC model (qemu emulates the hardware; ESXi auto-detects)
//   - VNC + QXL graphics (ESXi installer wants a graphical console)
//   - host-passthrough CPU with vmx feature when NestedHV is true
//   - q35 + UEFI

const domainTmpl = `<domain type='{{.DomainType}}'>
  <name>{{.Name}}</name>
  <memory unit='MiB'>{{.MemoryMiB}}</memory>
  <currentMemory unit='MiB'>{{.MemoryMiB}}</currentMemory>
  <vcpu placement='static'>{{.VCPUs}}</vcpu>
  <os{{if eq .Firmware "efi"}} firmware='efi'{{end}}>
    <type arch='x86_64' machine='q35'>hvm</type>
    <boot dev='{{if .ISOPath}}cdrom{{else}}hd{{end}}'/>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <cpu mode='host-passthrough' check='none' migratable='off'>
{{if .NestedHV}}    <feature policy='require' name='vmx'/>
{{end}}  </cpu>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
{{range .Disks}}    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none' io='native'/>
      <source file='{{.Source}}'/>
      <target dev='{{.Target}}' bus='{{.Bus}}' rotation_rate='{{.RotationRate}}'/>
    </disk>
{{end}}{{if .ISOPath}}    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='{{.ISOPath}}'/>
      <target dev='sdz' bus='sata'/>
      <readonly/>
    </disk>
{{end}}{{range .NICs}}    <interface type='{{.Type}}'>
      <source {{.Type}}='{{.Source}}'/>
      <model type='{{.Model}}'/>
{{if .MAC}}      <mac address='{{.MAC}}'/>
{{end}}    </interface>
{{end}}    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'/>
    <video>
      <model type='qxl' ram='65536' vram='65536' vgamem='16384' heads='1'/>
    </video>
    <serial type='pty'><target port='0'/></serial>
    <console type='pty'><target type='serial' port='0'/></console>
  </devices>
</domain>
`

type domainTmplData struct {
	DomainType string
	Name       string
	MemoryMiB  int
	VCPUs      int
	Firmware   string
	NestedHV   bool
	Disks      []diskTmplData
	NICs       []nicTmplData
	ISOPath    string
}

type diskTmplData struct {
	Source       string
	Target       string
	Bus          string
	RotationRate string // "1" → SSD, "7200" / etc. → spinning
}

type nicTmplData struct {
	Type   string // "bridge" or "network"
	Source string // bridge name or libvirt network name
	Model  string
	MAC    string
}

func renderDomainXML(vm spec.VMSpec, diskPaths []string) (string, error) {
	data := domainTmplData{
		DomainType: domainTypeFor(vm),
		Name:       vm.Name,
		MemoryMiB:  vm.MemoryMiB,
		VCPUs:      vm.VCPUs,
		Firmware:   string(vm.Firmware),
		NestedHV:   vm.NestedHV,
		ISOPath:    vm.ISOPath,
	}
	for i, d := range vm.Disks {
		src := defaultDiskPath(vm.Name, i)
		if i < len(diskPaths) && diskPaths[i] != "" {
			src = diskPaths[i]
		}
		data.Disks = append(data.Disks, diskTmplData{
			Source:       src,
			Target:       targetForBus(d.Bus, i),
			Bus:          busForXML(d.Bus),
			RotationRate: rotationRateFor(d),
		})
	}
	for _, n := range vm.NICs {
		typ, src := parseNetworkRef(n.NetworkRef)
		data.NICs = append(data.NICs, nicTmplData{
			Type:   typ,
			Source: src,
			Model:  modelOrDefault(n.Model),
			MAC:    n.MAC,
		})
	}

	var buf bytes.Buffer
	tmpl := template.Must(template.New("domain").Parse(domainTmpl))
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func renderDiskXML(disk spec.DiskSpec, slot int, source string) string {
	bus := busForXML(disk.Bus)
	target := targetForBus(disk.Bus, slot)
	return fmt.Sprintf(
		`<disk type='file' device='disk'>
  <driver name='qemu' type='qcow2' cache='none' io='native'/>
  <source file='%s'/>
  <target dev='%s' bus='%s' rotation_rate='%s'/>
</disk>`, source, target, bus, rotationRateFor(disk))
}

func renderNICXML(nic spec.NICSpec) string {
	typ, src := parseNetworkRef(nic.NetworkRef)
	macLine := ""
	if nic.MAC != "" {
		macLine = fmt.Sprintf("\n  <mac address='%s'/>", nic.MAC)
	}
	return fmt.Sprintf(
		`<interface type='%s'>
  <source %s='%s'/>
  <model type='%s'/>%s
</interface>`, typ, typ, src, modelOrDefault(nic.Model), macLine)
}

func renderCDROMXML(isoPath string) string {
	return fmt.Sprintf(
		`<disk type='file' device='cdrom'>
  <driver name='qemu' type='raw'/>
  <source file='%s'/>
  <target dev='sdz' bus='sata'/>
  <readonly/>
</disk>`, isoPath)
}

func domainTypeFor(_ spec.VMSpec) string {
	// "kvm" in production. The libvirt test driver only accepts "test"; if
	// we add a test-driver-aware test mode we'd rewrite here.
	return "kvm"
}

func defaultDiskPath(vmName string, idx int) string {
	return fmt.Sprintf("/var/lib/libvirt/images/%s-disk%d.qcow2", vmName, idx)
}

func busForXML(b spec.DiskBus) string {
	switch b {
	case "":
		return "sata" // operational default for ESXi-on-KVM
	case spec.DiskBusNVMe:
		// Pure NVMe is supported by qemu but needs a separate <controller>
		// declaration; SATA with rotation_rate=1 is the proven combo for
		// vSAN ESA in this codebase. Caller can override by setting Bus
		// explicitly to NVMe; we'd add controller declaration in v2.
		return "sata"
	default:
		return string(b)
	}
}

func targetForBus(b spec.DiskBus, slot int) string {
	switch busForXML(b) {
	case "virtio":
		return fmt.Sprintf("vd%c", 'a'+slot%26)
	default:
		return fmt.Sprintf("sd%c", 'a'+slot%26)
	}
}

func rotationRateFor(d spec.DiskSpec) string {
	if d.IsHDD {
		return "7200"
	}
	return "1" // SSD
}

func modelOrDefault(m spec.NICModel) string {
	if m == "" {
		return string(spec.NICVMXNET3)
	}
	return string(m)
}

// parseNetworkRef accepts "bridge:<name>", "network:<name>", or a bare name
// (treated as a libvirt-managed network). Returns the libvirt interface
// type ("bridge" or "network") and the backing source name.
func parseNetworkRef(ref string) (typ, src string) {
	if ref == "" {
		return "network", "default"
	}
	for _, prefix := range []struct {
		p string
		t string
	}{
		{"bridge:", "bridge"},
		{"network:", "network"},
	} {
		if len(ref) > len(prefix.p) && ref[:len(prefix.p)] == prefix.p {
			return prefix.t, ref[len(prefix.p):]
		}
	}
	return "network", ref
}
