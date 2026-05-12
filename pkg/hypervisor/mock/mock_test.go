package mock_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
	"github.com/svrc/cyberdeck/pkg/hypervisor/mock"
)

var _ = Describe("Mock backend", func() {
	conformance.HypervisorContract(func() (hypervisor.Hypervisor, conformance.Defaults, func()) {
		h := mock.New()
		return h, conformance.Defaults{ISOPath: "[mock]/CYBERDECK.iso"}, func() { _ = h.Close() }
	})
})
