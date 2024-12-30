package kubernetes_test

import (
	"fmt"
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

func createKumaDeployOptions(installMode InstallationMode, cni cniMode) []KumaDeploymentOption {
	opts := []KumaDeploymentOption{
		WithInstallationMode(installMode),
	}

	if installMode == HelmInstallationMode {
		opts = append(opts,
			WithHelmOpt("controlPlane.resources.requests.cpu", "1"),
			WithHelmOpt("controlPlane.resources.requests.memory", "2Gi"),
			WithHelmOpt("controlPlane.resources.limits.memory", "4Gi"),
			WithHelmChartPath(Config.HelmChartName),
			WithoutHelmOpt("global.image.tag"),
			WithHelmChartVersion(Config.KumaImageTag),
			WithHelmReleaseName(fmt.Sprintf("smoke-%s-%s", installMode, cni)),
		)
	} else {
		opts = append(opts,
			WithCtlOpts(map[string]string{
				"--set": "" +
					fmt.Sprintf("%scontrolPlane.resources.requests.cpu=1,", Config.HelmSubChartPrefix) +
					fmt.Sprintf("%scontrolPlane.resources.requests.memory=2Gi,", Config.HelmSubChartPrefix) +
					fmt.Sprintf("%scontrolPlane.resources.limits.memory=4Gi", Config.HelmSubChartPrefix),
			}))
	}

	if cni == cniEnabled {
		opts = append(opts, WithCNI())
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
	_ = Describe("Single Zone on Kubernetes", Install, Ordered)
)
