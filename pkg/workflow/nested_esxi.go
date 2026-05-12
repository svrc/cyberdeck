// Package workflow defines Temporal workflows for cyberdeck.
//
// CreateNestedESXi is the spike workflow: take a VMSpec, drive it through the
// Hypervisor interface as a sequence of activities. Each activity is its own
// retryable unit; the workflow itself is deterministic orchestration only.
//
// The split between workflow and activity is the key Temporal discipline:
//   - Workflow: pure decision logic. No time.Now, no rand, no I/O. Uses
//     workflow.Now / workflow.SideEffect / activity calls only.
//   - Activity: performs side effects. Must be idempotent (retried freely).
//
// Activities take a Hypervisor at construction time (Activities struct), not
// per-call — Temporal serializes activity inputs as JSON, and a Hypervisor
// connection isn't serializable.
package workflow

import (
	"context"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// TaskQueue is the Temporal task queue cyberdeck workers listen on.
const TaskQueue = "cyberdeck-spike"

// CreateNestedESXiInput is the workflow argument. JSON-serializable because
// Temporal stores it in workflow history.
//
// Spec is the boot shape — boot disk, boot NIC, optional install ISO —
// passed to CreateVM in one round trip. ExtraDisks and ExtraNICs are
// attached post-create. Splitting them this way makes the workflow's
// retry/restart story cleaner: if AttachDisk[2] fails, retrying that
// activity doesn't re-create the VM.
type CreateNestedESXiInput struct {
	Spec       spec.VMSpec
	ExtraDisks []spec.DiskSpec
	ExtraNICs  []spec.NICSpec
}

// CreateNestedESXiResult carries the post-create handle info back to the
// caller (and into history).
type CreateNestedESXiResult struct {
	VMID         string
	VMName       string
	AssignedMACs []string
}

// defaultActivityOptions: short timeouts since the spike activities all hit
// in-process or local backends. Real production workflows will want longer
// StartToCloseTimeout for OVA deploys etc.
func defaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	}
}

// CreateNestedESXi is the workflow. It calls activities in sequence:
//
//	CreateVM (boot shape) → AttachDisk* (extras) → AttachNIC* (extras) → PowerOn
//
// The ISO declared on Spec is attached during CreateVM. To attach an ISO
// after creation, callers should run a separate AttachISO activity.
func CreateNestedESXi(ctx workflow.Context, in CreateNestedESXiInput) (CreateNestedESXiResult, error) {
	ctx = workflow.WithActivityOptions(ctx, defaultActivityOptions())

	logger := workflow.GetLogger(ctx)
	logger.Info("CreateNestedESXi: starting", "vm", in.Spec.Name)

	var created CreateVMResult
	if err := workflow.ExecuteActivity(ctx, ActivityNameCreateVM, in.Spec).Get(ctx, &created); err != nil {
		return CreateNestedESXiResult{}, fmt.Errorf("CreateVM activity: %w", err)
	}

	for i, disk := range in.ExtraDisks {
		if err := workflow.ExecuteActivity(ctx, ActivityNameAttachDisk, AttachDiskInput{
			VMName: created.VMName,
			Disk:   disk,
		}).Get(ctx, nil); err != nil {
			return CreateNestedESXiResult{}, fmt.Errorf("AttachDisk[%d]: %w", i, err)
		}
	}

	macs := make([]string, 0, len(in.ExtraNICs))
	for i, nic := range in.ExtraNICs {
		var attached AttachNICResult
		if err := workflow.ExecuteActivity(ctx, ActivityNameAttachNIC, AttachNICInput{
			VMName: created.VMName,
			NIC:    nic,
		}).Get(ctx, &attached); err != nil {
			return CreateNestedESXiResult{}, fmt.Errorf("AttachNIC[%d]: %w", i, err)
		}
		macs = append(macs, attached.MAC)
	}

	if err := workflow.ExecuteActivity(ctx, ActivityNamePowerOn, PowerOnInput{
		VMName: created.VMName,
	}).Get(ctx, nil); err != nil {
		return CreateNestedESXiResult{}, fmt.Errorf("PowerOn: %w", err)
	}

	logger.Info("CreateNestedESXi: complete", "vm", created.VMName, "id", created.VMID)
	return CreateNestedESXiResult{
		VMID:         created.VMID,
		VMName:       created.VMName,
		AssignedMACs: macs,
	}, nil
}

// --- Activity inputs/outputs and names ---

const (
	ActivityNameCreateVM   = "CreateVM"
	ActivityNameAttachDisk = "AttachDisk"
	ActivityNameAttachNIC  = "AttachNIC"
	ActivityNameAttachISO  = "AttachISO"
	ActivityNamePowerOn    = "PowerOn"
)

type CreateVMResult struct {
	VMID   string
	VMName string
}
type AttachDiskInput struct {
	VMName string
	Disk   spec.DiskSpec
}
type AttachNICInput struct {
	VMName string
	NIC    spec.NICSpec
}
type AttachNICResult struct {
	MAC string
}
type AttachISOInput struct {
	VMName  string
	ISOPath string
}
type PowerOnInput struct {
	VMName string
}

// Activities holds a Hypervisor and exposes activity methods bound to it.
// Register with worker.RegisterActivity(activities) — Temporal will use the
// method names as activity names (matches the constants above).
type Activities struct {
	H hypervisor.Hypervisor
}

func (a *Activities) CreateVM(ctx context.Context, vm spec.VMSpec) (CreateVMResult, error) {
	ref, err := a.H.CreateVM(ctx, vm)
	if err != nil {
		return CreateVMResult{}, err
	}
	return CreateVMResult{VMID: ref.ID(), VMName: ref.Name()}, nil
}

func (a *Activities) AttachDisk(ctx context.Context, in AttachDiskInput) error {
	ref, err := a.H.Lookup(ctx, in.VMName)
	if err != nil {
		return err
	}
	return a.H.AttachDisk(ctx, ref, in.Disk)
}

func (a *Activities) AttachNIC(ctx context.Context, in AttachNICInput) (AttachNICResult, error) {
	ref, err := a.H.Lookup(ctx, in.VMName)
	if err != nil {
		return AttachNICResult{}, err
	}
	mac, err := a.H.AttachNIC(ctx, ref, in.NIC)
	if err != nil {
		return AttachNICResult{}, err
	}
	return AttachNICResult{MAC: mac}, nil
}

func (a *Activities) AttachISO(ctx context.Context, in AttachISOInput) error {
	ref, err := a.H.Lookup(ctx, in.VMName)
	if err != nil {
		return err
	}
	return a.H.AttachISO(ctx, ref, in.ISOPath)
}

func (a *Activities) PowerOn(ctx context.Context, in PowerOnInput) error {
	ref, err := a.H.Lookup(ctx, in.VMName)
	if err != nil {
		return err
	}
	return a.H.PowerOn(ctx, ref)
}
