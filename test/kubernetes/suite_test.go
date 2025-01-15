package kubernetes_test

import (
	"context"
	"fmt"
	"github.com/blang/semver/v4"
	cluster_providers "github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/eks"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/gke"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/kind"
	"github.com/kumahq/kuma-smoke/pkg/utils"
	"github.com/kumahq/kuma/pkg/test"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	"github.com/spf13/cobra"
	"os"
	"strings"
	"testing"
	"time"
)

func TestE2E(t *testing.T) {
	test.RunE2ESpecs(t, "Kuma Smoke Suite - Kubernetes")
}

var (
	_ = Describe("Single Zone on Kubernetes - Install", Install, Ordered)
	_ = Describe("Single Zone on Kubernetes - Upgrade", Upgrade, Ordered)
)

var cluster *K8sCluster
var targetVersion, prevMinorVersion, prevPatchVersion semver.Version
var kubeconfigPath string
var kubeConfigExportChannel chan struct{}

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

func exportKubeConfig(envType string, envName string, exportPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), utils.EnvironmentCreateTimeout)
	defer cancel()

	var dummyCmd *cobra.Command
	existingCls, err := cluster_providers.NewClusterFromExisting(envType, ctx, dummyCmd, envName)
	if err != nil {
		panic(fmt.Sprintf("Failed to get existing %s cluster %s: %v", envType, envName, err))
	}

	err = utils.WriteKubeconfig(envName, dummyCmd, existingCls.Config(), exportPath)
	if err != nil {
		panic(fmt.Sprintf("Failed to export kubeconfig for existing %s cluster %s: %v", envType, envName, err))
	}
}

func exportKubeConfigPeriodically(envType string, envName string, exportPath string) {
	kubeConfigExportChannel = make(chan struct{})
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-kubeConfigExportChannel:
			return
		case <-ticker.C:
			exportKubeConfig(envType, envName, exportPath)
		}
	}
}

var _ = SynchronizedBeforeSuite(func() {
	var err error
	targetVersion, err = semver.Parse(strings.TrimPrefix(Config.KumaImageTag, "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse test target version: %s", Config.KumaImageTag))
	}

	prevMinorVersion, err = semver.Parse(strings.TrimPrefix(os.Getenv("SMOKE_PRODUCT_VERSION_PREV_MINOR"), "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse previous minor version: %s", os.Getenv("SMOKE_PRODUCT_VERSION_PREV_MINOR")))
	}
	prevPatchVersion, err = semver.Parse(strings.TrimPrefix(os.Getenv("SMOKE_PRODUCT_VERSION_PREV_PATCH"), "v"))
	if err != nil {
		panic(fmt.Sprintf("Failed to parse previous patch version: %s", os.Getenv("SMOKE_PRODUCT_VERSION_PREV_PATCH")))
	}

	file, err := os.CreateTemp("", "kuma-smoke")
	if err != nil {
		panic(fmt.Sprintf("Failed to create temp file: %s", err))
	}
	_ = file.Close()
	kubeconfigPath = file.Name()
	cluster = NewK8sCluster(NewTestingT(), "kuma-smoke", true)
	cluster.WithKubeConfig(kubeconfigPath)
	fmt.Printf("using kubeconfig: %s\n", kubeconfigPath)

	envType := os.Getenv("SMOKE_ENV_TYPE")
	envName := os.Getenv("SMOKE_ENV_NAME")
	if envType == "" || envName == "" {
		panic("SMOKE_ENV_TYPE and SMOKE_ENV_NAME must be set to provide a running Kubernetes cluster")
	}

	exportKubeConfig(envType, envName, kubeconfigPath)
	go exportKubeConfigPeriodically(envType, envName, kubeconfigPath)
}, func() {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	close(kubeConfigExportChannel)
	_ = os.Remove(kubeconfigPath)
})
