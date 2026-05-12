package kvm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svrc/cyberdeck/internal/spec"
)

// TestRenderDomainXML_ESXiOnKVMShape asserts that the domain XML emitted for
// a typical nested ESXi spec carries the operationally-required settings:
// SATA + rotation_rate=1 (SSD), vmxnet3 NICs, host-passthrough CPU with vmx,
// VNC + QXL, q35 + UEFI, cache=none + io=native. These are the settings
// proven by the reference virt-install command for ESXi-on-KVM.
func TestRenderDomainXML_ESXiOnKVMShape(t *testing.T) {
	vm := spec.VMSpec{
		Name:      "esx-01a",
		VCPUs:     8,
		MemoryMiB: 32 * 1024,
		Firmware:  spec.FirmwareEFI,
		NestedHV:  true,
		GuestID:   "vmkernel7guest",
		Disks: []spec.DiskSpec{
			{SizeGiB: 32, Bus: spec.DiskBusSATA, Thin: true},
			{SizeGiB: 90, Bus: spec.DiskBusSATA, Thin: true},
		},
		NICs: []spec.NICSpec{
			{NetworkRef: "bridge:br-trunk", Model: spec.NICVMXNET3},
		},
		ISOPath: "/var/lib/libvirt/images/iso/CYBERDECK.iso",
	}

	xml, err := renderDomainXML(vm, nil)
	require.NoError(t, err)

	must := func(needle string) {
		t.Helper()
		assert.True(t, strings.Contains(xml, needle),
			"XML should contain %q. Got:\n%s", needle, xml)
	}
	must(`<domain type='kvm'>`)
	must(`<name>esx-01a</name>`)
	must(`<memory unit='MiB'>32768</memory>`)
	must(`<vcpu placement='static'>8</vcpu>`)
	must(`<type arch='x86_64' machine='q35'>hvm</type>`)
	must(`firmware='efi'`)
	must(`<boot dev='cdrom'/>`)
	must(`<cpu mode='host-passthrough' check='none' migratable='off'>`)
	must(`<feature policy='require' name='vmx'/>`)
	must(`<acpi/>`)
	must(`<apic/>`)
	// Disk settings
	must(`cache='none' io='native'`)
	must(`bus='sata'`)
	must(`rotation_rate='1'`)
	must(`/var/lib/libvirt/images/esx-01a-disk0.qcow2`)
	must(`/var/lib/libvirt/images/esx-01a-disk1.qcow2`)
	// CDROM
	must(`device='cdrom'`)
	must(`/var/lib/libvirt/images/iso/CYBERDECK.iso`)
	// NIC
	must(`<interface type='bridge'>`)
	must(`<source bridge='br-trunk'/>`)
	must(`<model type='vmxnet3'/>`)
	// Console
	must(`<graphics type='vnc'`)
	must(`<model type='qxl'`)
}

// TestParseNetworkRef covers the NetworkRef → libvirt interface-type/source
// translation used by both renderDomainXML and renderNICXML.
func TestParseNetworkRef(t *testing.T) {
	for _, tc := range []struct {
		in           string
		wantType     string
		wantSource   string
	}{
		{"bridge:br-trunk", "bridge", "br-trunk"},
		{"network:default", "network", "default"},
		{"VM Network", "network", "VM Network"}, // bare → libvirt network
		{"", "network", "default"},               // empty → safe default
	} {
		typ, src := parseNetworkRef(tc.in)
		assert.Equal(t, tc.wantType, typ, "type for %q", tc.in)
		assert.Equal(t, tc.wantSource, src, "source for %q", tc.in)
	}
}

// TestRotationRate covers the SSD-vs-HDD knob.
func TestRotationRate(t *testing.T) {
	assert.Equal(t, "1", rotationRateFor(spec.DiskSpec{}))                // default SSD
	assert.Equal(t, "1", rotationRateFor(spec.DiskSpec{IsHDD: false}))    // explicit SSD
	assert.Equal(t, "7200", rotationRateFor(spec.DiskSpec{IsHDD: true})) // spinning rust
}
