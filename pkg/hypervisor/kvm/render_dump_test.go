package kvm

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/svrc/cyberdeck/internal/spec"
)

// Developer-aid spec: dumps the exact XML cyberdeck would submit to libvirt
// for a full-fat 360 GiB / 40 vCPU ESXi domain. Output goes to GinkgoWriter
// so it's only displayed with `go test -v` or on failure. Run with:
//
//	go test -v -run TestKVMHypervisor -ginkgo.focus="dumps a full-fat"
var _ = Describe("renderDomainXML dump", Label("dump"), func() {
	It("dumps a full-fat ESXi-on-KVM domain XML for inspection", func() {
		xml, err := renderDomainXML(spec.VMSpec{
			Name:      "esxi9",
			VCPUs:     40,
			MemoryMiB: 360 * 1024,
			Firmware:  spec.FirmwareEFI,
			NestedHV:  true,
			GuestID:   "vmkernel7guest",
			Disks: []spec.DiskSpec{
				{SizeGiB: 512, Bus: spec.DiskBusSATA, Thin: true},
				{SizeGiB: 1536, Bus: spec.DiskBusSATA, Thin: true},
			},
			NICs: []spec.NICSpec{
				{NetworkRef: "bridge:br-trunk", Model: spec.NICVMXNET3},
				{NetworkRef: "bridge:br-trunk", Model: spec.NICVMXNET3},
			},
			ISOPath: "/mnt/VMware-VMvisor-Installer-9.0.2.0.25148076.x86_64.iso",
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		GinkgoWriter.Printf("\n--- rendered domain XML ---\n%s---\n", xml)
	})
})
