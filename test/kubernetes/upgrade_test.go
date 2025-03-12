package kubernetes_test

import (
	"encoding/json"
	"fmt"
	"github.com/blang/semver/v4"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/kumahq/kuma/test/framework/deployments/kic"
	"github.com/kumahq/kuma/test/framework/kumactl"
	"github.com/pkg/errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kumahq/kuma/pkg/config/core"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Upgrade() {
	demoApp := "demo-app"
	demoGateway := "demo-app-gateway"
	meshName := "upgrade"
	kicName := "kic"
	stabilizationDuration := 30 * time.Second

	DescribeTableSubtree("upgrade Kuma with a running workload", func(prevVersion semver.Version, installMode InstallationMode, cni cniMode) {
		if prevVersion.String() == targetVersion.String() {
			Logf("Skipping because the previous version is the same as the current version %s", targetVersion)
			return
		}
		if len(targetVersion.Pre) > 0 && installMode == HelmInstallationMode {
			Logf("Skipping because we don't have helm chart support for preview versions")
			return
		}
		targetVerKumactl := Config.KumactlBin
		targetVerImageTag := Config.KumaImageTag

		BeforeAll(func() {
			Logf("Testing upgrading from %s to %s", prevVersion, targetVersion)
			versionEnvName := "KUMACTLBIN_PREV_MINOR"
			if prevVersion.String() == prevPatchVersion.String() {
				versionEnvName = "KUMACTLBIN_PREV_PATCH"
			}
			prevKumactl := os.Getenv(versionEnvName)
			if installMode == KumactlInstallationMode {
				if prevKumactl == "" {
					Fail(fmt.Sprintf("Please set path to version %s kumactl using envirionment variable %s", prevVersion, versionEnvName))
					return
				}
				Config.KumactlBin = prevKumactl
				Config.KumaImageTag = prevVersion.String()
			} else {
				setupHelmRepo(cluster.GetTesting())
			}

			err := NewClusterSetup().
				Install(Kuma(core.Zone, createKumaDeployOptions(installMode, cni, prevVersion.String())...)).
				Install(NamespaceWithSidecarInjection(TestNamespace)).
				Setup(cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		E2EAfterAll(func() {
			Expect(cluster.DeleteNamespace(TestNamespace)).To(Succeed())
			Expect(cluster.DeleteNamespace(kicName)).To(Succeed())
			Expect(cluster.DeleteKuma()).To(Succeed())
			cluster.SetCP(nil)
			Config.KumactlBin = targetVerKumactl
			Config.KumaImageTag = targetVerImageTag
		})

		It("should run the demo app with mTLS and gateways", func() {
			By("install the demo app and wait for it to become ready")
			demoAppYAML, err := generateDemoAppYAML(cluster.GetKumactlOptions(), TestNamespace, Config.KumaNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(cluster.Install(YamlK8s(demoAppYAML))).To(Succeed())

			for _, fn := range []InstallFunc{
				WaitNumPods(TestNamespace, 1, demoApp),
				WaitPodsAvailable(TestNamespace, demoApp),
				WaitNumPods(TestNamespace, 1, demoGateway),
				WaitPodsAvailable(TestNamespace, demoGateway)} {
				Expect(fn(cluster)).To(Succeed())
			}

			By("enable mTLS on the mesh")
			Expect(cluster.Install(MTLSMeshKubernetes(meshName))).To(Succeed())

			By("install a open-by-default MeshTrafficPermission")
			Expect(cluster.Install(YamlK8s(meshTrafficPermission(Config.KumaNamespace)))).To(Succeed())

			By("deploy the Kong Gateway components")
			Expect(cluster.Install(GatewayAPICRDs)).To(Succeed())
			Expect(cluster.Install(NamespaceWithSidecarInjection(kicName))).To(Succeed())
			Expect(cluster.Install(kic.KongIngressController(
				kic.WithNamespace(kicName),
				kic.WithName(kicName),
				kic.WithMesh(meshName),
			))).To(Succeed())
			Expect(cluster.Install(kic.KongIngressService(
				kic.WithNamespace(kicName),
				kic.WithName(kicName),
			))).To(Succeed())
			kicIP, err := getServiceIP(cluster, kicName, "gateway")
			Expect(err).ToNot(HaveOccurred())

			By("install the GatewayAPI resources using Kong Gateway")
			Expect(cluster.Install(YamlK8s(demoAppGatewayResources(kicName, TestNamespace)))).To(Succeed())

			By("request the demo app via gateways")
			requestFromGateway(demoGateway, "", "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
				g.Expect(out).To(ContainSubstring("server: Kuma Gateway"))
			})
			requestFromGateway(demoGateway, kicIP, "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
			})
			dpList, err := getDataplaneList(cluster.GetKumactlOptions(), meshName)
			Expect(err).To(Not(HaveOccurred()))

			// wait for a stabilization period before checking for CP restarts
			time.Sleep(stabilizationDuration)
			Expect(CpRestarted(cluster)).To(BeFalse(), fmt.Sprintf("CP of version %s restarted, this should not happen.", prevVersion))

			prevCPLogOutputFile := filepath.Join(Config.DebugDir, fmt.Sprintf("%s-upgrade-logs-v%s-%s-%s.log",
				Config.KumaServiceName, prevVersion, installMode, cni))
			log1, err := cluster.GetKumaCPLogs()
			Expect(err).To(Not(HaveOccurred()))
			Expect(os.WriteFile(prevCPLogOutputFile, []byte(log1), 0o600)).To(Succeed())

			By(fmt.Sprintf("upgrade the CP from %s to %s", prevVersion, targetVersion))
			prevGwPod, err := PodOfApp(cluster, demoGateway, TestNamespace)
			Expect(err).ToNot(HaveOccurred())
			prevGWTemplateHash := prevGwPod.Labels["pod-template-hash"]

			prevCPPods := cluster.GetKuma().(*K8sControlPlane).GetKumaCPPods()
			prevCPTemplateHash := prevCPPods[0].Labels["pod-template-hash"]
			targetVerCPTemplateHash := ""

			// upgrade the CP to the new version (the target version of the testing)
			Config.KumactlBin = targetVerKumactl
			Config.KumaImageTag = targetVerImageTag
			cluster.GetKumactlOptions().Kumactl = Config.KumactlBin
			kumaDeployOpts := createKumaDeployOptions(installMode, cni, targetVersion.String())
			if installMode == KumactlInstallationMode {
				err = NewClusterSetup().
					Install(Kuma(core.Zone, kumaDeployOpts...)).
					Setup(cluster)
				Expect(err).ToNot(HaveOccurred())
			} else {
				err = cluster.UpgradeKuma(core.Zone, kumaDeployOpts...)
				Expect(err).ToNot(HaveOccurred())
			}

			By("waiting for the pods to be replaced by new version ones")
			// get the latest replicaset and make sure current version instances are available and previous version ones are scaled down to 0
			Eventually(func(g Gomega) {
				kubectlOpts := cluster.GetKubectlOptions(Config.KumaNamespace)
				cpDeploy, err := k8s.GetDeploymentE(cluster.GetTesting(), kubectlOpts, Config.KumaServiceName)
				g.Expect(err).ToNot(HaveOccurred())
				latestRsRevision := cpDeploy.Annotations["deployment.kubernetes.io/revision"]

				rsList := k8s.ListReplicaSets(cluster.GetTesting(), kubectlOpts, metav1.ListOptions{LabelSelector: "app=" + Config.KumaServiceName})
				for _, rs := range rsList {
					if rs.Annotations["deployment.kubernetes.io/revision"] == latestRsRevision {
						targetVerCPTemplateHash = rs.Labels["pod-template-hash"]
						break
					}
				}
				g.Expect(targetVerCPTemplateHash).ToNot(BeEmpty())
			}, "30s", "2s").ShouldNot(HaveOccurred(), "failed to find the latest ReplicaSet of the CP deployment")

			Eventually(func(g Gomega) {
				cpPods, err := k8s.ListPodsE(cluster.GetTesting(), cluster.GetKubectlOptions(Config.KumaNamespace),
					metav1.ListOptions{LabelSelector: fmt.Sprintf("pod-template-hash=%s", targetVerCPTemplateHash)})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(cpPods).To(HaveLen(1))
			}, "120s", "3s").ShouldNot(HaveOccurred(), "New version of CP pods are still starting")
			Eventually(func(g Gomega) {
				cpPods, err := k8s.ListPodsE(cluster.GetTesting(), cluster.GetKubectlOptions(Config.KumaNamespace),
					metav1.ListOptions{LabelSelector: fmt.Sprintf("pod-template-hash=%s", prevCPTemplateHash)})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(cpPods).To(HaveLen(0), "Previous version CP pods")

				gwPods, err := k8s.ListPodsE(cluster.GetTesting(), cluster.GetKubectlOptions(TestNamespace),
					metav1.ListOptions{LabelSelector: fmt.Sprintf("pod-template-hash=%s", prevGWTemplateHash)})
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(gwPods).To(HaveLen(0), "Previous version GW pods")
			}, "120s", "3s").ShouldNot(HaveOccurred(), "Previous version pods are still active")
			Expect(cluster.GetKuma().(*K8sControlPlane).FinalizeAdd()).To(Succeed())

			time.Sleep(stabilizationDuration)
			Expect(CpRestarted(cluster)).To(BeFalse(), fmt.Sprintf("CP of version %s restarted, this should not happen.", targetVersion))

			targetVersionCPLogOutputFile := filepath.Join(Config.DebugDir, fmt.Sprintf("%s-upgrade-logs-v%s-%s-%s.log",
				Config.KumaServiceName, targetVersion, installMode, cni))
			log2, err := cluster.GetKumaCPLogs()
			Expect(err).To(Not(HaveOccurred()))
			Expect(os.WriteFile(targetVersionCPLogOutputFile, []byte(log2), 0o600)).To(Succeed())

			By("request the demo app via gateways again")
			requestFromGateway(demoGateway, "", "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
				g.Expect(out).To(ContainSubstring("server: Kuma Gateway"))
			})
			requestFromGateway(demoGateway, kicIP, "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
			})

			dpList2, err := getDataplaneList(cluster.GetKumactlOptions(), meshName)
			Expect(err).To(Not(HaveOccurred()))
			Expect(dpList).To(Equal(dpList2), "dataplane list should be the same after the upgrade")
		})
	},
		Entry("kumactl, kuma-init (CNI disabled)", prevMinorVersion, KumactlInstallationMode, cniDisabled),
		Entry("helm, kuma-cni (CNI enabled)", prevPatchVersion, HelmInstallationMode, cniEnabled),
	)
}

func getDataplaneList(kumactlOpts *kumactl.KumactlOptions, mesh string) ([]string, error) {
	dpListJson, err := kumactlOpts.RunKumactlAndGetOutput("get", "dataplanes", "--mesh", mesh, "-ojson")

	if err != nil {
		return nil, errors.Wrap(err, "failed to execute kumactl get dataplanes")
	} else {
		dpResp := dataplaneListResponse{}
		var dpNames []string
		if jsonErr := json.Unmarshal([]byte(dpListJson), &dpResp); jsonErr != nil {
			return nil, errors.Wrap(jsonErr, "json Unmarshal dataplane list failed with error")
		}
		for _, dpObj := range dpResp.Items {
			dpNames = append(dpNames, dpObj.Name)
		}
		return dpNames, nil
	}
}

type dataplaneResponse struct {
	Mesh string `json:"mesh"`
	Name string `json:"name"`
}

type dataplaneListResponse struct {
	Total int                 `json:"total"`
	Items []dataplaneResponse `json:"items"`
}
