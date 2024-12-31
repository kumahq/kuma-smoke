package kubernetes_test

import (
	"fmt"
	"github.com/blang/semver/v4"
	"github.com/kumahq/kuma/pkg/test"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	"os"
	"strings"
	"testing"
)

func TestE2E(t *testing.T) {
	test.RunE2ESpecs(t, "Kuma Smoke Suite - Kubernetes")
}

var cluster *K8sCluster
var currentVersion, prevMinorVersion, prevPatchVersion semver.Version

func createKumaDeployOptions(installMode InstallationMode, cni cniMode, version string) []KumaDeploymentOption {
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
			WithHelmChartVersion(version),
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

func init() {
	var err error
	currentVersion, err = semver.Parse(strings.TrimPrefix(Config.KumaImageTag, "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse current version: %s", Config.KumaImageTag))
	}

	prevMinorVersion, err = semver.Parse(strings.TrimPrefix(os.Getenv("SMOKE_PRODUCT_VERSION_PREV_MINOR"), "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse previous minor version: %s", os.Getenv("SMOKE_PRODUCT_VERSION_PREV_MINOR")))
	}
	prevPatchVersion, err = semver.Parse(strings.TrimPrefix(os.Getenv("SMOKE_PRODUCT_VERSION_PREV_PATCH"), "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse previous patch version: %s", os.Getenv("SMOKE_PRODUCT_VERSION_PREV_PATCH")))
	}
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
	_ = Describe("Single Zone on Kubernetes - Install", Install, Ordered)
	_ = Describe("Single Zone on Kubernetes - Upgrade", Upgrade, Ordered)
)
