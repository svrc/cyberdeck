package kvm_test

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
	"github.com/svrc/cyberdeck/pkg/hypervisor/kvm"
)

// TestConformance runs the Hypervisor conformance suite against a reachable
// libvirtd. Skips if no LIBVIRT_URI is set — keeps `go test ./...` green on
// a Mac dev box without libvirt. See README "Layer B" for setup.
//
// Required env vars when running:
//   - LIBVIRT_URI:    e.g., "qemu:///system" — driver URI for libvirtd
//   - LIBVIRT_SOCKET: path to a libvirt Unix socket reachable from this host.
//                     Use SSH socket forward for remote KVM hosts:
//                       ssh -fN -L /tmp/cyberdeck-libvirt.sock:/var/run/libvirt/libvirt-sock user@host
//   - LIBVIRT_TEST_ISO: path on the libvirt host to a file usable as a CDROM
//                     (libvirt validates existence at PowerOn time). Create
//                     once with:
//                       virsh vol-create-as default cyberdeck-test.iso 64K --format raw
//                     then set LIBVIRT_TEST_ISO=$(virsh vol-path --pool default cyberdeck-test.iso).
func TestConformance(t *testing.T) {
	uri := os.Getenv("LIBVIRT_URI")
	if uri == "" {
		t.Skip("LIBVIRT_URI not set; skipping KVM conformance (expected on a Mac dev box without libvirt)")
	}
	isoPath := os.Getenv("LIBVIRT_TEST_ISO")
	if isoPath == "" {
		t.Skip("LIBVIRT_TEST_ISO not set; skipping KVM conformance (need a real file path on the libvirt host for AttachISO)")
	}

	conformance.Run(t, func(t *testing.T) (hypervisor.Hypervisor, conformance.Defaults) {
		conn := dialLibvirt(t, uri)
		h, err := kvm.New(conn)
		if err != nil {
			t.Fatalf("kvm.New: %v", err)
		}
		// LIFO: t.Cleanup registered here runs AFTER per-VM destroys
		// registered later by the conformance suite.
		t.Cleanup(func() { _ = h.Close() })
		return h, conformance.Defaults{ISOPath: isoPath}
	})
}

func dialLibvirt(t *testing.T, uri string) *libvirt.Libvirt {
	t.Helper()
	dialer := dialers.NewLocal(dialers.WithSocket(libvirtSocketFor(uri)))
	conn := libvirt.NewWithDialer(dialer)

	if err := conn.ConnectToURI(libvirt.ConnectURI(uri)); err != nil {
		t.Skipf("could not connect to libvirtd at %s: %v (is libvirtd running?)", uri, err)
	}
	t.Cleanup(func() { _ = conn.Disconnect() })
	return conn
}

// libvirtSocketFor picks a sensible local socket for the given URI. For
// session URIs we use the per-user socket; everything else falls back to the
// system socket. Tests that need a remote libvirt should set LIBVIRT_SOCKET
// explicitly.
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

// guard against the import-only "net" usage being flagged. (Future change may
// introduce direct net.Dialer use; keeping the import documented.)
var _ = net.Dial
var _ = time.Second
var _ = context.Background
