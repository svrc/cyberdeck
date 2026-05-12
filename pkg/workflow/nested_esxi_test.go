package workflow_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/svrc/cyberdeck/internal/spec"
	hvmock "github.com/svrc/cyberdeck/pkg/hypervisor/mock"
	"github.com/svrc/cyberdeck/pkg/workflow"
)

// sampleInput returns a workflow input with a boot-shape Spec plus one extra
// disk and one extra NIC, so specs can verify both the CreateVM path and the
// post-create attach path execute correctly.
func sampleInput(name string) workflow.CreateNestedESXiInput {
	return workflow.CreateNestedESXiInput{
		Spec: spec.VMSpec{
			Name:      name,
			VCPUs:     8,
			MemoryMiB: 32 * 1024,
			Firmware:  spec.FirmwareEFI,
			NestedHV:  true,
			GuestID:   "vmkernel7guest",
			Disks: []spec.DiskSpec{
				{SizeGiB: 32, Bus: spec.DiskBusSCSI, Thin: true}, // boot
			},
			NICs: []spec.NICSpec{
				{NetworkRef: "VM Network", Model: spec.NICE1000e},
			},
			ISOPath: "[datastore1]/iso/CYBERDECK.iso",
		},
		ExtraDisks: []spec.DiskSpec{
			{SizeGiB: 90, Bus: spec.DiskBusSCSI, Thin: true}, // vSAN cache
		},
		ExtraNICs: []spec.NICSpec{
			{NetworkRef: "vmotion", Model: spec.NICE1000e},
		},
	}
}

var _ = Describe("CreateNestedESXi workflow", func() {
	var (
		env *testsuite.TestWorkflowEnvironment
		h   *hvmock.Hypervisor
	)

	BeforeEach(func() {
		suite := testsuite.WorkflowTestSuite{}
		env = suite.NewTestWorkflowEnvironment()
		h = hvmock.New()
	})

	// Layer A integration: the workflow's full activity sequence executes
	// against the mock Hypervisor wired through real Activities. No real
	// infrastructure is touched; we assert the recorded VM matches the
	// expected end-state.
	Context("end-to-end against the mock hypervisor through real activities", func() {
		It("creates the VM, attaches extras, powers it on, and records every operation", func() {
			env.RegisterActivity(&workflow.Activities{H: h})

			env.ExecuteWorkflow(workflow.CreateNestedESXi, sampleInput("esx-01a"))

			Expect(env.IsWorkflowCompleted()).To(BeTrue(), "workflow should complete")
			Expect(env.GetWorkflowError()).ToNot(HaveOccurred())

			var result workflow.CreateNestedESXiResult
			Expect(env.GetWorkflowResult(&result)).To(Succeed())
			Expect(result.VMName).To(Equal("esx-01a"))
			Expect(result.VMID).ToNot(BeEmpty())

			vm := h.Get("esx-01a")
			Expect(vm).ToNot(BeNil(), "mock should have recorded the VM")
			Expect(vm.PoweredOn).To(BeTrue(), "VM should end powered on")
			Expect(vm.Disks).To(HaveLen(2), "boot disk + post-create vSAN disk")
			Expect(vm.NICs).To(HaveLen(2), "boot NIC + post-create vmotion NIC")
			Expect(vm.ISOPaths).To(Equal([]string{"[datastore1]/iso/CYBERDECK.iso"}))
		})
	})

	// Layer A workflow-only test: replaces activities with mocks and asserts
	// workflow control flow without exercising any Hypervisor code. Useful
	// when we add branching logic (e.g. conditionally attach an ISO based
	// on workflow input).
	//
	// Pattern: register the real Activities struct so the testsuite knows
	// the activity types and signatures, then OnActivity intercepts each
	// call before the real method runs. The Activities struct's Hypervisor
	// is never touched.
	Context("workflow control flow with mocked activities", func() {
		It("emits the create→attach→power-on sequence and aggregates assigned MACs", func() {
			env.RegisterActivity(&workflow.Activities{H: h})

			env.OnActivity(workflow.ActivityNameCreateVM, mock.Anything, mock.Anything).
				Return(workflow.CreateVMResult{VMID: "vm-42", VMName: "esx-01a"}, nil)
			env.OnActivity(workflow.ActivityNameAttachDisk, mock.Anything, mock.Anything).Return(nil)
			env.OnActivity(workflow.ActivityNameAttachNIC, mock.Anything, mock.Anything).
				Return(workflow.AttachNICResult{MAC: "00:50:56:01:02:03"}, nil)
			env.OnActivity(workflow.ActivityNamePowerOn, mock.Anything, mock.Anything).Return(nil)

			env.ExecuteWorkflow(workflow.CreateNestedESXi, sampleInput("esx-01a"))

			Expect(env.IsWorkflowCompleted()).To(BeTrue())
			Expect(env.GetWorkflowError()).ToNot(HaveOccurred())

			var result workflow.CreateNestedESXiResult
			Expect(env.GetWorkflowResult(&result)).To(Succeed())
			Expect(result.VMID).To(Equal("vm-42"))
			Expect(result.VMName).To(Equal("esx-01a"))
			// One extra NIC beyond the boot NIC ⇒ one MAC recorded.
			Expect(result.AssignedMACs).To(Equal([]string{"00:50:56:01:02:03"}))
		})
	})
})
