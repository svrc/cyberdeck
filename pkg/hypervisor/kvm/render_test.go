package kvm

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/svrc/cyberdeck/internal/spec"
)

var _ = Describe("renderDomainXML for ESXi-on-KVM", func() {
	var xml string

	BeforeEach(func() {
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

		var err error
		xml, err = renderDomainXML(vm, nil)
		Expect(err).ToNot(HaveOccurred())
	})

	// Each It below pins one operationally-load-bearing setting from the
	// reference virt-install command Stu has proven against ESXi-on-KVM.
	// If any of these regress, ESXi will fail to install or run.

	It("emits a KVM domain with the right name and resources", func() {
		Expect(xml).To(ContainSubstring(`<domain type='kvm'>`))
		Expect(xml).To(ContainSubstring(`<name>esx-01a</name>`))
		Expect(xml).To(ContainSubstring(`<memory unit='MiB'>32768</memory>`))
		Expect(xml).To(ContainSubstring(`<vcpu placement='static'>8</vcpu>`))
	})

	It("uses q35 + UEFI firmware so ESXi recognises the platform", func() {
		Expect(xml).To(ContainSubstring(`<type arch='x86_64' machine='q35'>hvm</type>`))
		Expect(xml).To(ContainSubstring(`firmware='efi'`))
		Expect(xml).To(ContainSubstring(`<boot dev='cdrom'/>`))
	})

	It("passes host CPU through with vmx for nested virtualisation", func() {
		Expect(xml).To(ContainSubstring(`<cpu mode='host-passthrough' check='none' migratable='off'>`))
		Expect(xml).To(ContainSubstring(`<feature policy='require' name='vmx'/>`))
		Expect(xml).To(ContainSubstring(`<acpi/>`))
		Expect(xml).To(ContainSubstring(`<apic/>`))
	})

	It("attaches disks on SATA with cache=none/io=native and SSD rotation_rate", func() {
		Expect(xml).To(ContainSubstring(`cache='none' io='native'`))
		Expect(xml).To(ContainSubstring(`bus='sata'`))
		Expect(xml).To(ContainSubstring(`rotation_rate='1'`))
		Expect(xml).To(ContainSubstring(`/var/lib/libvirt/images/esx-01a-disk0.qcow2`))
		Expect(xml).To(ContainSubstring(`/var/lib/libvirt/images/esx-01a-disk1.qcow2`))
	})

	It("attaches the install ISO as a CDROM", func() {
		Expect(xml).To(ContainSubstring(`device='cdrom'`))
		Expect(xml).To(ContainSubstring(`/var/lib/libvirt/images/iso/CYBERDECK.iso`))
	})

	It("uses vmxnet3 NICs via a bridge interface", func() {
		Expect(xml).To(ContainSubstring(`<interface type='bridge'>`))
		Expect(xml).To(ContainSubstring(`<source bridge='br-trunk'/>`))
		Expect(xml).To(ContainSubstring(`<model type='vmxnet3'/>`))
	})

	It("exposes a VNC console with a QXL framebuffer", func() {
		Expect(xml).To(ContainSubstring(`<graphics type='vnc'`))
		Expect(xml).To(ContainSubstring(`<model type='qxl'`))
	})
})

var _ = Describe("parseNetworkRef", func() {
	DescribeTable("translates NetworkRef shorthand into libvirt interface type+source",
		func(in, wantType, wantSource string) {
			typ, src := parseNetworkRef(in)
			Expect(typ).To(Equal(wantType), "type for %q", in)
			Expect(src).To(Equal(wantSource), "source for %q", in)
		},
		Entry("explicit bridge", "bridge:br-trunk", "bridge", "br-trunk"),
		Entry("explicit libvirt network", "network:default", "network", "default"),
		Entry("bare name routes to a libvirt network", "VM Network", "network", "VM Network"),
		Entry("empty falls back to the default network", "", "network", "default"),
	)
})

var _ = Describe("rotationRateFor (the SSD-vs-HDD knob)", func() {
	It("defaults to SSD when IsHDD is unset", func() {
		Expect(rotationRateFor(spec.DiskSpec{})).To(Equal("1"))
	})
	It("returns 1 for explicit SSDs", func() {
		Expect(rotationRateFor(spec.DiskSpec{IsHDD: false})).To(Equal("1"))
	})
	It("returns 7200 for spinning rust", func() {
		Expect(rotationRateFor(spec.DiskSpec{IsHDD: true})).To(Equal("7200"))
	})
})
