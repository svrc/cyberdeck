// Command cyberdeck is the spike CLI. Drives one CreateNestedESXi workflow
// against a chosen Hypervisor backend.
//
// For the spike we run the workflow in-process via the Temporal testsuite —
// no `temporal server start-dev` needed. A future revision will add a
// `--temporal <addr>` flag that connects to a real Temporal worker.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"github.com/spf13/cobra"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"
	"go.temporal.io/sdk/testsuite"

	"github.com/svrc/cyberdeck/internal/spec"
	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/kvm"
	hvmock "github.com/svrc/cyberdeck/pkg/hypervisor/mock"
	"github.com/svrc/cyberdeck/pkg/hypervisor/vsphere"
	"github.com/svrc/cyberdeck/pkg/workflow"
)

func main() {
	root := &cobra.Command{
		Use:   "cyberdeck",
		Short: "Cyberdeck — VMware Cloud Foundation lab orchestrator (Go)",
	}
	root.AddCommand(spikeCmd(), serverCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func spikeCmd() *cobra.Command {
	var backend string
	var vmName string
	var temporalAddr string

	cmd := &cobra.Command{
		Use:   "spike",
		Short: "Run one CreateNestedESXi workflow against a chosen backend",
		Long: "spike drives the CreateNestedESXi Temporal workflow end-to-end against\n" +
			"the selected Hypervisor backend, reporting the resulting VM ID/name and any\n" +
			"assigned MACs.\n\n" +
			"Workflow runtime:\n" +
			"  Default                  in-process Temporal testsuite (no server needed).\n" +
			"  --temporal HOST:PORT     real Temporal server. Run `temporal server start-dev`\n" +
			"                           first; cyberdeck spawns an in-process worker for the\n" +
			"                           duration of the call. For long-running deployments,\n" +
			"                           run a separate `cyberdeck server` worker instead.\n\n" +
			"Backends:\n" +
			"  mock      In-memory; always works.\n" +
			"  vsphere   In-process govmomi simulator by default. Set VCENTER_HOST/USER/PASS\n" +
			"            (and optional VCENTER_DC/CLUSTER/DATASTORE/NETWORK) to talk to a\n" +
			"            real vCenter.\n" +
			"  kvm       Connects to libvirtd via $LIBVIRT_URI (default qemu:///session).\n" +
			"            Set $LIBVIRT_SOCKET to override the local socket path\n" +
			"            (useful for SSH-forwarded sockets to a remote KVM host).",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			h, cleanup, err := openBackend(ctx, backend)
			if err != nil {
				return fmt.Errorf("open backend %q: %w", backend, err)
			}
			defer cleanup()

			result, runtime, err := runSpike(ctx, h, vmName, backend, temporalAddr)
			if err != nil {
				return err
			}
			fmt.Printf("runtime:    %s\n", runtime)
			fmt.Printf("backend:    %s\n", backend)
			fmt.Printf("vm name:    %s\n", result.VMName)
			fmt.Printf("vm id:      %s\n", result.VMID)
			fmt.Printf("extra macs: %v\n", result.AssignedMACs)
			return nil
		},
	}
	cmd.Flags().StringVar(&backend, "backend", "mock", "hypervisor backend: mock | vsphere | kvm")
	cmd.Flags().StringVar(&vmName, "name", "esx-spike-01a", "name for the nested ESXi VM")
	cmd.Flags().StringVar(&temporalAddr, "temporal", "", "Temporal server address (e.g. localhost:7233); empty => in-process testsuite")
	return cmd
}

func serverCmd() *cobra.Command {
	var backend string
	var temporalAddr string

	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run a long-lived Temporal worker for cyberdeck workflows",
		Long: `server registers the cyberdeck workflows and activities with a Temporal
server and blocks, processing workflow executions until SIGINT/SIGTERM.

Requires a running Temporal server (e.g. ` + "`temporal server start-dev`" + `).
Backend flag selects which Hypervisor the activities act on.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if temporalAddr == "" {
				return fmt.Errorf("--temporal is required for server mode")
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			h, cleanup, err := openBackend(ctx, backend)
			if err != nil {
				return err
			}
			defer cleanup()

			c, err := workflow.NewClient(temporalAddr)
			if err != nil {
				return fmt.Errorf("connect temporal %s: %w", temporalAddr, err)
			}
			defer c.Close()

			fmt.Fprintf(os.Stderr, "cyberdeck server: backend=%s temporal=%s task-queue=%s\n",
				backend, temporalAddr, workflow.TaskQueue)
			return workflow.RunWorker(ctx, c, h)
		},
	}
	cmd.Flags().StringVar(&backend, "backend", "mock", "hypervisor backend: mock | vsphere | kvm")
	cmd.Flags().StringVar(&temporalAddr, "temporal", "localhost:7233", "Temporal server address")
	return cmd
}

func openBackend(ctx context.Context, backend string) (hypervisor.Hypervisor, func(), error) {
	switch backend {
	case "mock":
		h := hvmock.New()
		return h, func() { _ = h.Close() }, nil

	case "vsphere":
		// If VCENTER_HOST is set, talk to a real vCenter; else stand up an
		// in-process simulator. Same code path either way past this point.
		if host := os.Getenv("VCENTER_HOST"); host != "" {
			user, pass := os.Getenv("VCENTER_USER"), os.Getenv("VCENTER_PASS")
			if user == "" || pass == "" {
				return nil, nil, fmt.Errorf("VCENTER_HOST set but VCENTER_USER/PASS missing")
			}
			u, err := url.Parse(fmt.Sprintf("https://%s/sdk", host))
			if err != nil {
				return nil, nil, err
			}
			u.User = url.UserPassword(user, pass)
			client, err := govmomi.NewClient(ctx, u, true)
			if err != nil {
				return nil, nil, fmt.Errorf("connect %s: %w", host, err)
			}
			h, err := vsphere.New(ctx, vsphere.Config{
				Client:     client,
				Datacenter: os.Getenv("VCENTER_DC"),
				Cluster:    os.Getenv("VCENTER_CLUSTER"),
				Datastore:  os.Getenv("VCENTER_DATASTORE"),
				Network:    os.Getenv("VCENTER_NETWORK"),
			})
			if err != nil {
				_ = client.Logout(ctx)
				return nil, nil, err
			}
			return h, func() {
				_ = h.Close()
				_ = client.Logout(ctx)
			}, nil
		}

		model := simulator.VPX()
		if err := model.Create(); err != nil {
			return nil, nil, fmt.Errorf("simulator.Create: %w", err)
		}
		server := model.Service.NewServer()
		client, err := govmomi.NewClient(ctx, server.URL, true)
		if err != nil {
			server.Close()
			model.Remove()
			return nil, nil, err
		}
		h, err := vsphere.New(ctx, vsphere.Config{Client: client})
		if err != nil {
			_ = client.Logout(ctx)
			server.Close()
			model.Remove()
			return nil, nil, err
		}
		cleanup := func() {
			_ = h.Close()
			_ = client.Logout(ctx)
			server.Close()
			model.Remove()
		}
		return h, cleanup, nil

	case "kvm":
		uri := os.Getenv("LIBVIRT_URI")
		if uri == "" {
			uri = "qemu:///session"
		}
		// LIBVIRT_SOCKET overrides the default Unix socket path. Useful for
		// SSH-forwarded sockets (e.g. /tmp/cyberdeck-libvirt.sock) so we can
		// reach a remote libvirtd via the local-dialer code path.
		var dopts []dialers.LocalOption
		if sock := os.Getenv("LIBVIRT_SOCKET"); sock != "" {
			dopts = append(dopts, dialers.WithSocket(sock))
		}
		dialer := dialers.NewLocal(dopts...)
		conn := libvirt.NewWithDialer(dialer)
		if err := conn.ConnectToURI(libvirt.ConnectURI(uri)); err != nil {
			return nil, nil, fmt.Errorf("connect %s: %w", uri, err)
		}
		h, err := kvm.New(conn)
		if err != nil {
			_ = conn.Disconnect()
			return nil, nil, err
		}
		return h, func() {
			_ = h.Close()
			_ = conn.Disconnect()
		}, nil

	default:
		return nil, nil, fmt.Errorf("unknown backend %q (try mock | vsphere | kvm)", backend)
	}
}

// runSpike drives the workflow against either an in-process Temporal
// testsuite (default — no server needed) or a real Temporal server when
// temporalAddr is set. The runtime label returned identifies which path ran.
func runSpike(ctx context.Context, h hypervisor.Hypervisor, vmName, backend, temporalAddr string) (workflow.CreateNestedESXiResult, string, error) {
	in := workflow.CreateNestedESXiInput{
		Spec:       buildSpec(vmName, backend),
		ExtraDisks: extraDisks(),
		ExtraNICs:  extraNICs(backend),
	}

	if temporalAddr != "" {
		c, err := workflow.NewClient(temporalAddr)
		if err != nil {
			return workflow.CreateNestedESXiResult{}, "", fmt.Errorf("connect temporal %s: %w", temporalAddr, err)
		}
		defer c.Close()

		// Use vmName as the workflow ID — gives us free dedup if the user
		// re-runs the same spike, and makes the Temporal UI history easy to
		// find by VM name.
		result, err := workflow.RunOnce(ctx, c, h, "cyberdeck-spike-"+vmName, in)
		if err != nil {
			return workflow.CreateNestedESXiResult{}, "", err
		}
		return result, fmt.Sprintf("temporal@%s", temporalAddr), nil
	}

	// In-process testsuite path — no server, no worker.
	suite := testsuite.WorkflowTestSuite{}
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivity(&workflow.Activities{H: h})
	env.ExecuteWorkflow(workflow.CreateNestedESXi, in)

	if !env.IsWorkflowCompleted() {
		return workflow.CreateNestedESXiResult{}, "", fmt.Errorf("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		return workflow.CreateNestedESXiResult{}, "", fmt.Errorf("workflow error: %w", err)
	}
	var result workflow.CreateNestedESXiResult
	if err := env.GetWorkflowResult(&result); err != nil {
		return workflow.CreateNestedESXiResult{}, "", fmt.Errorf("decode result: %w", err)
	}
	return result, "in-process testsuite", nil
}

// buildSpec returns the boot-shape ESXi spec. Backend differences:
//   - vSphere wants a portgroup name and a "[datastore1]/path" ISO.
//   - KVM wants a host bridge ref and a local ISO path. Production setups
//     use bridge:br-trunk for trunked VLANs (or bridge:br-vxlan for VXLAN
//     overlay) per the operational reference command.
//   - mock just records whatever it's given.
//
// Sizes are smaller than the operational command (40 vCPU / 360 GiB / 512+1536
// GiB disks) so the spike runs comfortably on a laptop.
func buildSpec(vmName, backend string) spec.VMSpec {
	s := spec.VMSpec{
		Name:      vmName,
		VCPUs:     8,
		MemoryMiB: 32 * 1024,
		Firmware:  spec.FirmwareEFI,
		NestedHV:  true,
		GuestID:   "vmkernel7guest",
		Disks: []spec.DiskSpec{
			{SizeGiB: 32, Bus: spec.DiskBusSATA, Thin: true},
		},
	}
	switch backend {
	case "kvm":
		// "default" is the libvirt-managed network present on every standard
		// install. Bridge mode (bridge:br-trunk) is what production setups
		// use; switch via env or flag once the spike grows up.
		s.NICs = []spec.NICSpec{{NetworkRef: "default", Model: spec.NICVMXNET3}}
		// LIBVIRT_TEST_ISO points at a real file on the libvirt host. Without
		// it we leave ISOPath empty and the domain boots from disk (which
		// will spin in UEFI shell — fine for a "did the workflow run" demo,
		// not fine for an actual ESXi install).
		s.ISOPath = os.Getenv("LIBVIRT_TEST_ISO")
	case "vsphere":
		// Network ref: simulator has "VM Network"; real vCenter is set via
		// VCENTER_NETWORK (which the vsphere.Config also picks as the default).
		// Setting NetworkRef "" lets the impl fall back to its configured
		// default network — which is exactly what we want.
		s.NICs = []spec.NICSpec{{NetworkRef: "", Model: spec.NICVMXNET3}}
		// Same story for ISO: VCENTER_TEST_ISO if you have one, else skip.
		s.ISOPath = os.Getenv("VCENTER_TEST_ISO")
	}
	return s
}

func extraDisks() []spec.DiskSpec {
	// Mirrors the legacy ESXiBuild.psm1 OSA shape: 90 GiB cache + 2× 450 GiB
	// capacity. Real cyberdeck deployments will compute these from the vSAN
	// mode + sizing config.
	return []spec.DiskSpec{
		{SizeGiB: 90, Bus: spec.DiskBusSATA, Thin: true},
		{SizeGiB: 450, Bus: spec.DiskBusSATA, Thin: true},
		{SizeGiB: 450, Bus: spec.DiskBusSATA, Thin: true},
	}
}

func extraNICs(backend string) []spec.NICSpec {
	var netRef string
	switch backend {
	case "kvm":
		netRef = "default"
	case "mock":
		netRef = "mock-net"
	case "vsphere":
		// Empty → impl falls back to its configured default network. Works
		// for both simulator and real-vCenter modes since vsphere.Config
		// resolves Network at New() time.
		netRef = ""
	}
	return []spec.NICSpec{
		{NetworkRef: netRef, Model: spec.NICVMXNET3},
		{NetworkRef: netRef, Model: spec.NICVMXNET3},
		{NetworkRef: netRef, Model: spec.NICVMXNET3},
	}
}
