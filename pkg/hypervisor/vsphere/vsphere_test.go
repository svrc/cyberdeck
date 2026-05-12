package vsphere_test

import (
	"context"
	"fmt"
	"net/url"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/simulator"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
	"github.com/svrc/cyberdeck/pkg/hypervisor/vsphere"
)

var _ = Describe("vSphere backend", func() {

	// Against the in-process govmomi simulator: a SOAP-compliant vCenter
	// implementation used by Terraform's vSphere provider and the vSphere
	// CSI driver. VPX() simulates a vCenter-shaped inventory (1 DC, 1
	// cluster, 1 host, 1 datastore, 1 network). Runs in `go test` with no
	// external dependencies.
	Context("against the govmomi simulator", Label("simulator"), func() {
		conformance.HypervisorContract(func() (hypervisor.Hypervisor, conformance.Defaults, func()) {
			ctx, cancel := context.WithCancel(context.Background())

			model := simulator.VPX()
			Expect(model.Create()).To(Succeed(), "simulator.Create")
			server := model.Service.NewServer()

			client, err := govmomi.NewClient(ctx, server.URL, true)
			Expect(err).ToNot(HaveOccurred(), "govmomi.NewClient")

			h, err := vsphere.New(ctx, vsphere.Config{Client: client})
			Expect(err).ToNot(HaveOccurred(), "vsphere.New")

			cleanup := func() {
				_ = h.Close()
				_ = client.Logout(ctx)
				server.Close()
				model.Remove()
				cancel()
			}
			return h, conformance.Defaults{ISOPath: "[LocalDS_0]/iso/CYBERDECK.iso"}, cleanup
		})
	})

	// Against a live vCenter. Skips when env vars are not set, so the suite
	// stays green on a dev box without lab access.
	//
	// Required env vars:
	//
	//	VCENTER_HOST     vCenter hostname or IP (e.g. vc01.vcf.lab)
	//	VCENTER_USER     SSO user (e.g. administrator@vsphere.local)
	//	VCENTER_PASS     password
	//
	// Optional placement overrides (defaults match Stu's VCF lab):
	//
	//	VCENTER_DC        Datacenter name        (default: VCF-Datacenter)
	//	VCENTER_CLUSTER   Cluster name           (default: VCF-Mgmt-Cluster)
	//	VCENTER_DATASTORE Datastore name         (default: local-vmfs-datastore-1)
	//	VCENTER_NETWORK   Portgroup name         (default: DVPG_FOR_VM_MANAGEMENT)
	//	VCENTER_TEST_ISO  Datastore-style path to an ISO usable as CDROM
	//	                   (e.g. "[local-vmfs-datastore-1]/iso/cyberdeck-test.iso").
	//	                   If unset, AttachISO is skipped (PowerOn still runs).
	Context("against a real vCenter", Label("real-infra", "real-vcenter"), func() {
		conformance.HypervisorContract(func() (hypervisor.Hypervisor, conformance.Defaults, func()) {
			host, user, pass := os.Getenv("VCENTER_HOST"), os.Getenv("VCENTER_USER"), os.Getenv("VCENTER_PASS")
			if host == "" || user == "" || pass == "" {
				Skip("VCENTER_HOST/USER/PASS not all set; skipping real-vCenter conformance")
			}
			dc := envOr("VCENTER_DC", "VCF-Datacenter")
			cluster := envOr("VCENTER_CLUSTER", "VCF-Mgmt-Cluster")
			datastore := envOr("VCENTER_DATASTORE", "local-vmfs-datastore-1")
			network := envOr("VCENTER_NETWORK", "DVPG_FOR_VM_MANAGEMENT")
			isoPath := os.Getenv("VCENTER_TEST_ISO")

			ctx, cancel := context.WithCancel(context.Background())

			u, err := url.Parse(fmt.Sprintf("https://%s/sdk", host))
			Expect(err).ToNot(HaveOccurred(), "parse vCenter URL")
			u.User = url.UserPassword(user, pass)

			client, err := govmomi.NewClient(ctx, u, true) // insecure: lab vCenter typically self-signed
			Expect(err).ToNot(HaveOccurred(), "connect %s", host)

			h, err := vsphere.New(ctx, vsphere.Config{
				Client:     client,
				Datacenter: dc,
				Cluster:    cluster,
				Datastore:  datastore,
				Network:    network,
			})
			Expect(err).ToNot(HaveOccurred(), "vsphere.New")

			cleanup := func() {
				_ = h.Close()
				_ = client.Logout(ctx)
				cancel()
			}
			return h, conformance.Defaults{ISOPath: isoPath}, cleanup
		})
	})
})

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
