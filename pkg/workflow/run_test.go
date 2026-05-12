package workflow_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/svrc/cyberdeck/internal/spec"
	hvmock "github.com/svrc/cyberdeck/pkg/hypervisor/mock"
	"github.com/svrc/cyberdeck/pkg/workflow"
)

// TestRunOnce_AgainstRealTemporal exercises the full real-Temporal path:
// connects to a running Temporal server, spins up an in-process worker,
// submits a workflow, waits for the result, asserts the recorded VM
// matches expectations.
//
// Skips when TEMPORAL_ADDR is not set — keeps the suite green on a dev
// box without Temporal installed. Run with:
//
//	temporal server start-dev &
//	TEMPORAL_ADDR=localhost:7233 go test -run RunOnce ./pkg/workflow/...
func TestRunOnce_AgainstRealTemporal(t *testing.T) {
	addr := os.Getenv("TEMPORAL_ADDR")
	if addr == "" {
		t.Skip("TEMPORAL_ADDR not set; skipping real-Temporal worker test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	c, err := workflow.NewClient(addr)
	require.NoError(t, err, "connect Temporal at %s", addr)
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

	// Use a unique workflow ID per test invocation so re-runs don't collide
	// with Temporal's id-reuse policy. Since RunOnce starts an in-process
	// worker, no separate cyberdeck server has to be running.
	wfID := "cd-runonce-test-" + time.Now().UTC().Format("20060102T150405.000")
	result, err := workflow.RunOnce(ctx, c, h, wfID, in)
	require.NoError(t, err)
	assert.Equal(t, "cd-runonce-test", result.VMName)

	// Verify the mock recorded the expected shape — proves activities
	// actually ran against our hypervisor (not e.g. mocked at the workflow
	// layer).
	vm := h.Get("cd-runonce-test")
	require.NotNil(t, vm, "mock should have recorded the VM")
	assert.True(t, vm.PoweredOn)
	assert.Len(t, vm.Disks, 2, "boot + 1 extra")
	assert.Len(t, vm.NICs, 2, "boot + 1 extra")
}
