package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/svrc/cyberdeck/internal/spec"
	hvmock "github.com/svrc/cyberdeck/pkg/hypervisor/mock"
	"github.com/svrc/cyberdeck/pkg/workflow"
)

// sampleInput returns a workflow input with a boot-shape Spec plus one extra
// disk and one extra NIC, so we can verify both the CreateVM path and the
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

// TestCreateNestedESXi_EndToEndAgainstMock runs the workflow against the mock
// Hypervisor wired through real Activities. This is a Layer A integration
// test: the workflow's full activity sequence executes, but no real infra is
// touched. Asserts the recorded VM matches the expected shape.
func TestCreateNestedESXi_EndToEndAgainstMock(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	h := hvmock.New()
	acts := &workflow.Activities{H: h}
	env.RegisterActivity(acts)

	in := sampleInput("esx-01a")
	env.ExecuteWorkflow(workflow.CreateNestedESXi, in)

	require.True(t, env.IsWorkflowCompleted(), "workflow should complete")
	require.NoError(t, env.GetWorkflowError(), "workflow should not error")

	var result workflow.CreateNestedESXiResult
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "esx-01a", result.VMName)
	assert.NotEmpty(t, result.VMID)

	// Inspect what the mock recorded.
	vm := h.Get("esx-01a")
	require.NotNil(t, vm, "mock should have recorded the VM")
	assert.True(t, vm.PoweredOn, "VM should end powered on")
	assert.Len(t, vm.Disks, 2, "should have boot disk + post-create vSAN disk")
	assert.Len(t, vm.NICs, 2, "should have boot NIC + post-create vmotion NIC")
	assert.Equal(t, []string{"[datastore1]/iso/CYBERDECK.iso"}, vm.ISOPaths)
}

// TestCreateNestedESXi_WorkflowLogic_MockedActivities is a Layer A *workflow*
// test: replaces activities with mocks and asserts the workflow control flow
// without exercising any Hypervisor code. Useful when we add branching logic
// (e.g., conditionally attach an ISO based on workflow input).
//
// Pattern: we register the real Activities struct (so the testsuite knows the
// activity types and signatures), then OnActivity intercepts each call before
// the real method runs. The Activities struct's Hypervisor is never used.
func TestCreateNestedESXi_WorkflowLogic_MockedActivities(t *testing.T) {
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()

	env.RegisterActivity(&workflow.Activities{H: hvmock.New()})

	env.OnActivity(workflow.ActivityNameCreateVM, mock.Anything, mock.Anything).
		Return(workflow.CreateVMResult{VMID: "vm-42", VMName: "esx-01a"}, nil)
	env.OnActivity(workflow.ActivityNameAttachDisk, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(workflow.ActivityNameAttachNIC, mock.Anything, mock.Anything).
		Return(workflow.AttachNICResult{MAC: "00:50:56:01:02:03"}, nil)
	env.OnActivity(workflow.ActivityNamePowerOn, mock.Anything, mock.Anything).Return(nil)

	in := sampleInput("esx-01a")
	env.ExecuteWorkflow(workflow.CreateNestedESXi, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var result workflow.CreateNestedESXiResult
	require.NoError(t, env.GetWorkflowResult(&result))
	assert.Equal(t, "vm-42", result.VMID)
	assert.Equal(t, "esx-01a", result.VMName)
	// One extra NIC beyond the boot NIC ⇒ one MAC recorded.
	assert.Equal(t, []string{"00:50:56:01:02:03"}, result.AssignedMACs)
}

