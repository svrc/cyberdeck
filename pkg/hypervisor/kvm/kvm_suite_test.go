package kvm

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestKVMHypervisor(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "KVM hypervisor suite")
}
