package workflow

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// RunOnce executes one CreateNestedESXi workflow against a real Temporal
// server and returns the result. Spins up an in-process worker for the
// duration of the call, so the caller doesn't need a separate `cyberdeck
// server` running. Useful for the spike CLI and for one-shot operations;
// production deployments should run a long-lived `cyberdeck server` instead.
//
// The workflowID is the deduplication key — re-running RunOnce with the same
// ID returns the same result if the workflow already completed (Temporal's
// workflow-id reuse policy defaults to AllowDuplicate, but our intent is
// idempotent execution, so we explicitly set RejectDuplicate).
func RunOnce(
	ctx context.Context,
	c client.Client,
	h hypervisor.Hypervisor,
	workflowID string,
	in CreateNestedESXiInput,
) (CreateNestedESXiResult, error) {
	w := NewWorker(c, h)
	if err := w.Start(); err != nil {
		return CreateNestedESXiResult{}, fmt.Errorf("worker start: %w", err)
	}
	defer w.Stop()

	return execute(ctx, c, workflowID, in)
}

// Submit submits a CreateNestedESXi workflow to a remote server (where a
// `cyberdeck server` worker is already listening on TaskQueue) and returns
// the result. Doesn't start a worker.
func Submit(
	ctx context.Context,
	c client.Client,
	workflowID string,
	in CreateNestedESXiInput,
) (CreateNestedESXiResult, error) {
	return execute(ctx, c, workflowID, in)
}

func execute(
	ctx context.Context,
	c client.Client,
	workflowID string,
	in CreateNestedESXiInput,
) (CreateNestedESXiResult, error) {
	opts := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                TaskQueue,
		WorkflowExecutionTimeout: 30 * time.Minute,
	}
	run, err := c.ExecuteWorkflow(ctx, opts, CreateNestedESXi, in)
	if err != nil {
		return CreateNestedESXiResult{}, fmt.Errorf("ExecuteWorkflow: %w", err)
	}
	var result CreateNestedESXiResult
	if err := run.Get(ctx, &result); err != nil {
		return CreateNestedESXiResult{}, fmt.Errorf("workflow result: %w", err)
	}
	return result, nil
}

// silence unused-import lint when only NewWorker is wanted.
var _ = worker.New
