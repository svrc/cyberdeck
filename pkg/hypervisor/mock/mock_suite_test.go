package mock_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMockHypervisor(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Mock hypervisor suite")
}
