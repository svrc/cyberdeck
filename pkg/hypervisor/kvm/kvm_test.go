package kvm

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
)

// Hypervisor contract against a reachable libvirtd. Skips if LIBVIRT_URI is
// not set, so the suite stays green on a Mac dev box without libvirt. See
// docs/testing.md "Layer B" for setup.
//
// Required env vars when running:
//
//	LIBVIRT_URI:      e.g., "qemu:///system" — driver URI for libvirtd
//	LIBVIRT_SOCKET:   path to a libvirt Unix socket reachable from this
//	                  host. For remote KVM hosts use an SSH socket forward:
//	                    ssh -fN -L /tmp/cyberdeck-libvirt.sock:/var/run/libvirt/libvirt-sock user@host
//	LIBVIRT_TEST_ISO: path on the libvirt host to a file usable as a
//	                  CDROM (libvirt validates existence at PowerOn time).
//	                  Create once with:
//	                    virsh vol-create-as default cyberdeck-test.iso 64K --format raw
//	                  then set
//	                    LIBVIRT_TEST_ISO=$(virsh vol-path --pool default cyberdeck-test.iso)
var _ = Describe("KVM backend", func() {
	Context("against a real libvirtd", Label("real-infra", "real-libvirt"), func() {
		conformance.HypervisorContract(func() (hypervisor.Hypervisor, conformance.Defaults, func()) {
			uri := os.Getenv("LIBVIRT_URI")
			if uri == "" {
				Skip("LIBVIRT_URI not set; skipping KVM conformance (expected on a Mac dev box without libvirt)")
			}
			isoPath := os.Getenv("LIBVIRT_TEST_ISO")
			if isoPath == "" {
				Skip("LIBVIRT_TEST_ISO not set; skipping KVM conformance (need a real file path on the libvirt host for AttachISO)")
			}

			dialer := dialers.NewLocal(dialers.WithSocket(libvirtSocketFor(uri)))
			conn := libvirt.NewWithDialer(dialer)
			if err := conn.ConnectToURI(libvirt.ConnectURI(uri)); err != nil {
				Skip("could not connect to libvirtd at " + uri + ": " + err.Error() + " (is libvirtd running?)")
			}

			h, err := New(conn)
			Expect(err).ToNot(HaveOccurred(), "kvm.New")

			cleanup := func() {
				_ = h.Close()
				_ = conn.Disconnect()
			}
			return h, conformance.Defaults{ISOPath: isoPath}, cleanup
		})
	})
})

// libvirtSocketFor picks a sensible local socket for the given URI. For
// session URIs we use the per-user socket; everything else falls back to the
// system socket. Tests that need a remote libvirt should set LIBVIRT_SOCKET.
func libvirtSocketFor(uri string) string {
	if sock := os.Getenv("LIBVIRT_SOCKET"); sock != "" {
		return sock
	}
	if strings.Contains(uri, "session") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + "/.cache/libvirt/libvirt-sock"
		}
	}
	return "/var/run/libvirt/libvirt-sock"
}
