package kubernetes_test

import (
	"github.com/kumahq/kuma/pkg/test"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	"os"
	"testing"
)

func TestE2E(t *testing.T) {
	test.RunE2ESpecs(t, "Kuma Smoke Suite - Kubernetes")
}

var cluster *K8sCluster

func defaultKumaOptions() []KumaDeploymentOption {
	opts := []KumaDeploymentOption{
		WithCtlOpts(map[string]string{
			"--set": "" +
				"kuma.controlPlane.resources.requests.cpu=1," +
				"kuma.controlPlane.resources.requests.memory=2Gi," +
				"kuma.controlPlane.resources.limits.memory=4Gi",
		}),
	}

	kmeshLicensePath := os.Getenv("KMESH_LICENSE_PATH")
	if kmeshLicensePath != "" {
		opts = append(opts,
			WithCtlOpts(map[string]string{
				"--license-path": kmeshLicensePath,
			}))
	}

	return opts
}

var _ = BeforeSuite(func() {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	if kubeConfigPath == "" {
		kubeConfigPath = "${HOME}/.kube/config"
	}

	cluster = NewK8sCluster(NewTestingT(), "kuma-smoke", true)
	cluster.WithKubeConfig(os.ExpandEnv(kubeConfigPath))
})

var (
	_ = Describe("Single Zone", Install, Ordered)
)
