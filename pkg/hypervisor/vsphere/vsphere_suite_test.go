package vsphere_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestVSphereHypervisor(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "vSphere hypervisor suite")
}
