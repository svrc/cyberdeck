package mock_test

import (
	"testing"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
	"github.com/svrc/cyberdeck/pkg/hypervisor/conformance"
	"github.com/svrc/cyberdeck/pkg/hypervisor/mock"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) (hypervisor.Hypervisor, conformance.Defaults) {
		h := mock.New()
		t.Cleanup(func() { _ = h.Close() })
		return h, conformance.Defaults{ISOPath: "[mock]/CYBERDECK.iso"}
	})
}
