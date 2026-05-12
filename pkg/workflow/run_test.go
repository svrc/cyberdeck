package workflow_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/svrc/cyberdeck/internal/spec"
	hvmock "github.com/svrc/cyberdeck/pkg/hypervisor/mock"
	"github.com/svrc/cyberdeck/pkg/workflow"
)

// Real-Temporal integration: connects to a running Temporal server, spins
// up an in-process worker via RunOnce, submits a workflow, waits for the
// result, asserts the recorded VM matches expectations.
//
// Skips when TEMPORAL_ADDR is not set. Run with:
//
//	temporal server start-dev &
//	TEMPORAL_ADDR=localhost:7233 go test ./pkg/workflow/... \
//	    -ginkgo.label-filter=real-temporal
var _ = Describe("RunOnce against a real Temporal server", Label("real-infra", "real-temporal"), func() {
	It("starts an in-process worker, runs CreateNestedESXi end-to-end, and tears down", func() {
		addr := os.Getenv("TEMPORAL_ADDR")
		if addr == "" {
			Skip("TEMPORAL_ADDR not set; skipping real-Temporal worker test")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		c, err := workflow.NewClient(addr)
		Expect(err).ToNot(HaveOccurred(), "connect Temporal at %s", addr)
		defer c.Close()

		h := hvmock.New()
		in := workflow.CreateNestedESXiInput{
			Spec: spec.VMSpec{
				Name:      "cd-runonce-test",
				VCPUs:     2,
				MemoryMiB: 2048,
				Firmware:  spec.FirmwareEFI,
				NestedHV:  true,
				GuestID:   "vmkernel7guest",
				Disks:     []spec.DiskSpec{{SizeGiB: 4, Bus: spec.DiskBusSATA, Thin: true}},
				NICs:      []spec.NICSpec{{NetworkRef: "default", Model: spec.NICVMXNET3}},
			},
			ExtraDisks: []spec.DiskSpec{{SizeGiB: 8, Bus: spec.DiskBusSATA, Thin: true}},
			ExtraNICs:  []spec.NICSpec{{NetworkRef: "default", Model: spec.NICVMXNET3}},
		}

		// Unique workflow ID per invocation so re-runs don't collide with
		// Temporal's id-reuse policy. RunOnce starts an in-process worker,
		// so no separate cyberdeck server is needed.
		wfID := "cd-runonce-test-" + time.Now().UTC().Format("20060102T150405.000")
		result, err := workflow.RunOnce(ctx, c, h, wfID, in)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.VMName).To(Equal("cd-runonce-test"))

		// Verify the mock recorded the expected shape — proves activities
		// actually ran against our hypervisor (not e.g. mocked at the
		// workflow layer).
		vm := h.Get("cd-runonce-test")
		Expect(vm).ToNot(BeNil(), "mock should have recorded the VM")
		Expect(vm.PoweredOn).To(BeTrue())
		Expect(vm.Disks).To(HaveLen(2), "boot + 1 extra")
		Expect(vm.NICs).To(HaveLen(2), "boot + 1 extra")
	})
})
