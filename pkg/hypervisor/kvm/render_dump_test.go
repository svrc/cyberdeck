package kvm

import (
	"fmt"
	"testing"

	"github.com/svrc/cyberdeck/internal/spec"
)

// TestDumpRenderedXML is a developer-aid: run with `go test -v -run TestDump`
// to see the exact XML cyberdeck would submit to libvirt. Not an assertion.
func TestDumpRenderedXML(t *testing.T) {
	if testing.Short() {
		t.Skip("dump is verbose; skipped under -short")
	}
	xml, err := renderDomainXML(spec.VMSpec{
		Name:      "esxi9",
		VCPUs:     40,
		MemoryMiB: 360 * 1024, // 368640
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
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("\n--- rendered domain XML ---\n%s---\n", xml)
}
