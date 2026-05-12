package vsphere_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
	"github.com/svrc/cyberdeck/pkg/hypervisor/vsphere"
)

// TestConformance_Simulator runs the full Hypervisor conformance suite
// against an in-process govmomi/simulator. No real vCenter, no Docker — the
// simulator is a SOAP-compliant vCenter implementation that lives inside the
// test process. Used by Terraform's vSphere provider, Kubernetes vSphere CSI,
// etc.
func TestConformance_Simulator(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (hypervisor.Hypervisor, conformance.Defaults) {
		ctx, cancel := context.WithCancel(context.Background())

		// VPX() ⇒ a vCenter-shaped simulated inventory: 1 DC, 1 cluster, 1
		// host, 1 datastore, 1 network. ESX() would simulate a standalone
		// host — we'd run conformance separately in that mode in a fuller
		// suite.
		model := simulator.VPX()
		if err := model.Create(); err != nil {
			cancel()
			t.Fatalf("simulator.Create: %v", err)
		}
		server := model.Service.NewServer()

		client, err := govmomi.NewClient(ctx, server.URL, true)
		if err != nil {
			server.Close()
			model.Remove()
			cancel()
			t.Fatalf("govmomi.NewClient: %v", err)
		}

		h, err := vsphere.New(ctx, vsphere.Config{Client: client})
		if err != nil {
			_ = client.Logout(ctx)
			server.Close()
			model.Remove()
			cancel()
			t.Fatalf("vsphere.New: %v", err)
		}

		// Register cleanup via t.Cleanup so it runs AFTER the conformance
		// suite's per-VM destroys (LIFO order).
		t.Cleanup(func() {
			_ = h.Close()
			_ = client.Logout(ctx)
			server.Close()
			model.Remove()
			cancel()
		})
		return h, conformance.Defaults{ISOPath: "[LocalDS_0]/iso/CYBERDECK.iso"}
	})
}

// TestConformance_RealVCenter runs the suite against a live vCenter. Skips
// when env vars are not set, so `go test ./...` stays green on a dev box
// without lab access.
//
// Required env vars:
//
//	VCENTER_HOST     vCenter hostname or IP (e.g. vc01.vcf.lab)
//	VCENTER_USER     SSO user (e.g. administrator@vsphere.local)
//	VCENTER_PASS     password
//
// Optional placement overrides — defaults match Stu's VCF lab:
//
//	VCENTER_DC        Datacenter name        (default: VCF-Datacenter)
//	VCENTER_CLUSTER   Cluster name           (default: VCF-Mgmt-Cluster)
//	VCENTER_DATASTORE Datastore name         (default: local-vmfs-datastore-1)
//	VCENTER_NETWORK   Portgroup name         (default: DVPG_FOR_VM_MANAGEMENT)
//	VCENTER_TEST_ISO  Datastore-style path to an ISO usable as CDROM
//	                   (e.g. "[local-vmfs-datastore-1]/iso/cyberdeck-test.iso").
//	                   If unset, AttachISO is skipped (PowerOn still runs).
func TestConformance_RealVCenter(t *testing.T) {
	host, user, pass := os.Getenv("VCENTER_HOST"), os.Getenv("VCENTER_USER"), os.Getenv("VCENTER_PASS")
	if host == "" || user == "" || pass == "" {
		t.Skip("VCENTER_HOST/USER/PASS not all set; skipping real-vCenter conformance")
	}
	dc := envOr("VCENTER_DC", "VCF-Datacenter")
	cluster := envOr("VCENTER_CLUSTER", "VCF-Mgmt-Cluster")
	datastore := envOr("VCENTER_DATASTORE", "local-vmfs-datastore-1")
	network := envOr("VCENTER_NETWORK", "DVPG_FOR_VM_MANAGEMENT")
	isoPath := os.Getenv("VCENTER_TEST_ISO")

	conformance.Run(t, func(t *testing.T) (hypervisor.Hypervisor, conformance.Defaults) {
		ctx, cancel := context.WithCancel(context.Background())

		u, err := url.Parse(fmt.Sprintf("https://%s/sdk", host))
		if err != nil {
			cancel()
			t.Fatalf("parse url: %v", err)
		}
		u.User = url.UserPassword(user, pass)

		client, err := govmomi.NewClient(ctx, u, true) // insecure: lab vCenter typically self-signed
		if err != nil {
			cancel()
			t.Fatalf("connect %s: %v", host, err)
		}

		h, err := vsphere.New(ctx, vsphere.Config{
			Client:    client,
			Datacenter: dc,
			Cluster:    cluster,
			Datastore:  datastore,
			Network:    network,
		})
		if err != nil {
			_ = client.Logout(ctx)
			cancel()
			t.Fatalf("vsphere.New: %v", err)
		}

		t.Cleanup(func() {
			_ = h.Close()
			_ = client.Logout(ctx)
			cancel()
		})
		return h, conformance.Defaults{ISOPath: isoPath}
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
