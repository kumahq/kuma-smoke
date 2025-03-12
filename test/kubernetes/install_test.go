package kubernetes_test

import (
	"fmt"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/retry"
	"github.com/gruntwork-io/terratest/modules/testing"
	"github.com/kumahq/kuma/test/framework/deployments/kic"
	"github.com/kumahq/kuma/test/framework/kumactl"
	"github.com/pkg/errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/kumahq/kuma/pkg/config/core"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type cniMode string

const (
	cniDisabled cniMode = "kuma-init"
	cniEnabled  cniMode = "kuma-cni"
)

func Install() {
	demoApp := "demo-app"
	demoGateway := "demo-app-gateway"
	defaultMesh := "default"
	kicName := "kic"

	DescribeTableSubtree("install Kuma and run smoke scenarios", func(installMode InstallationMode, cni cniMode) {
		if len(targetVersion.Pre) > 0 && installMode == HelmInstallationMode {
			Logf("Skipping because we don't have helm chart support for preview versions")
			return
		}

		BeforeAll(func() {
			if installMode == HelmInstallationMode {
				setupHelmRepo(cluster.GetTesting())
			}

			err := NewClusterSetup().
				Install(Kuma(core.Zone, createKumaDeployOptions(installMode, cni, Config.KumaImageTag)...)).
				Install(NamespaceWithSidecarInjection(TestNamespace)).
				Setup(cluster)
			Expect(err).ToNot(HaveOccurred())
		})

		E2EAfterAll(func() {
			Expect(cluster.DeleteNamespace(TestNamespace)).To(Succeed())
			Expect(cluster.DeleteNamespace(kicName)).To(Succeed())
			Expect(cluster.DeleteKuma()).To(Succeed())
		})

		It("should deploy mesh wide policy", func() {
			policy := `
apiVersion: kuma.io/v1alpha1
kind: MeshRateLimit
metadata:
  name: mesh-rate-limit
  namespace: %s
spec:
  targetRef:
    kind: Mesh
    proxyTypes:
      - Sidecar
  from:
    - targetRef:
        kind: Mesh
      default:
        local:
          http:
            requestRate:
              num: 10000
              interval: 1s
            onRateLimit:
              status: 429
`
			Expect(cluster.Install(YamlK8s(fmt.Sprintf(policy, Config.KumaNamespace)))).To(Succeed())
		})

		It("should support running the demo app with builtin gateway", func() {
			By("install the demo app and wait for it to become ready")
			kumactlOpts := NewKumactlOptionsE2E(cluster.GetTesting(), cluster.Name(), true)
			demoAppYAML, err := generateDemoAppYAML(kumactlOpts, TestNamespace, Config.KumaNamespace)
			Expect(err).ToNot(HaveOccurred())
			Expect(cluster.Install(YamlK8s(demoAppYAML))).To(Succeed())

			for _, fn := range []InstallFunc{
				WaitNumPods(TestNamespace, 1, demoApp),
				WaitPodsAvailable(TestNamespace, demoApp),
				WaitNumPods(TestNamespace, 1, demoGateway),
				WaitPodsAvailable(TestNamespace, demoGateway)} {
				Expect(fn(cluster)).To(Succeed())
			}

			By("request the demo app from the gateway pod")
			requestFromGateway(demoGateway, "", "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
				g.Expect(out).To(ContainSubstring("server: Kuma Gateway"))

				responseLog := filepath.Join(Config.DebugDir, fmt.Sprintf("demo-app-wget-%s-%s.log", installMode, cni))
				Expect(os.WriteFile(responseLog, []byte(out), 0o600)).To(Succeed())
			})
		})

		It("should distribute certs when mTLS is enabled", func() {
			By("enable mTLS on the mesh")
			Expect(cluster.Install(MTLSMeshKubernetes(defaultMesh))).To(Succeed())

			Eventually(func(g Gomega) {
				out, err := k8s.RunKubectlAndGetOutputE(
					cluster.GetTesting(),
					cluster.GetKubectlOptions(),
					"get", "meshinsights", defaultMesh, "-ojsonpath='{.spec.mTLS.issuedBackends.ca-1.total}'",
				)

				g.Expect(err).ToNot(HaveOccurred())
				number, err := strconv.Atoi(strings.Trim(out, "'"))
				g.Expect(err).ToNot(HaveOccurred())
				Expect(number).To(BeNumerically(">", 0))
			}, "60s", "1s").Should(Succeed())

			By("the demo-app should not be requested without a MeshTrafficPermission applied")
			requestFromGateway(demoGateway, "", "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("403 Forbidden"))
			})

			By("the demo-app should be requested successfully with a MeshTrafficPermission applied")
			Expect(cluster.Install(YamlK8s(meshTrafficPermission(Config.KumaNamespace)))).To(Succeed())

			By("request the demo app")
			requestFromGateway(demoGateway, "", "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
			})
		})

		It("should support delegated gateway", func() {
			By("install the gateway API CRD")
			Expect(cluster.Install(GatewayAPICRDs)).To(Succeed())

			By("deploy the Kong Gateway components")
			Expect(cluster.Install(NamespaceWithSidecarInjection(kicName))).To(Succeed())
			Expect(cluster.Install(kic.KongIngressController(
				kic.WithNamespace(kicName),
				kic.WithName(kicName),
				kic.WithMesh(defaultMesh),
			))).To(Succeed())
			Expect(cluster.Install(kic.KongIngressService(
				kic.WithNamespace(kicName),
				kic.WithName(kicName),
			))).To(Succeed())
			kicIP, err := getServiceIP(cluster, kicName, "gateway")
			Expect(err).ToNot(HaveOccurred())

			By("install the GatewayAPI resources using Kong Gateway")
			Expect(cluster.Install(YamlK8s(demoAppGatewayResources(kicName, TestNamespace)))).To(Succeed())

			By("request the demo app via the delegated gateway")
			requestFromGateway(demoGateway, kicIP, "/", func(g Gomega, out string) {
				g.Expect(out).To(ContainSubstring("200 OK"))
			})
		})

		It("should maintain a stable control plane", func() {
			time.Sleep(10 * time.Second)

			Expect(CpRestarted(cluster)).To(BeFalse(), cluster.Name()+" restarted in this suite, this should not happen.")

			logOutputFile := filepath.Join(Config.DebugDir, fmt.Sprintf("%s-install-logs-%s-%s.log",
				Config.KumaServiceName, installMode, cni))
			logs, err := cluster.GetKumaCPLogs()

			Expect(err).To(Not(HaveOccurred()))
			Expect(os.WriteFile(logOutputFile, []byte(logs), 0o600)).To(Succeed())
		})
	},
		Entry("kumactl, kuma-init (CNI disabled)", KumactlInstallationMode, cniDisabled),
		Entry("helm, kuma-cni (CNI enabled)", HelmInstallationMode, cniEnabled),
	)
}

func generateDemoAppYAML(kumactlOpts *kumactl.KumactlOptions, appNamespace, kumaNamespace string) (string, error) {
	demoAppYAML, err := kumactlOpts.RunKumactlAndGetOutput("install", "demo",
		"--namespace", appNamespace,
		"--system-namespace", kumaNamespace)
	if err != nil {
		return "", err
	}

	// in 2.8.x and older versions, the YAML generated from "kumactl install demo" has a bug in their MeshHTTPRoute resources
	// causing the kuma.io/service tag fail to support changing app namespace
	demoAppYAML = strings.Replace(demoAppYAML,
		"demo-app-gateway_kuma-demo_svc",
		fmt.Sprintf("demo-app-gateway_%s_svc", appNamespace), -1)
	return demoAppYAML, nil
}

func meshTrafficPermission(namespace string) string {
	mtp := `
apiVersion: kuma.io/v1alpha1
kind: MeshTrafficPermission
metadata:
  name: allow-any
  namespace: %s
spec:
  targetRef:
    kind: Mesh
  from:
    - targetRef:
        kind: Mesh
      default:
        action: Allow`
	return fmt.Sprintf(mtp, namespace)
}

func demoAppGatewayResources(kic, appNamespace string) string {
	ingress := `
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: KIC_NAME
  annotations:
    konghq.com/gatewayclass-unmanaged: 'true'
spec:
  controllerName: konghq.com/kic-gateway-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: kong
  namespace: APP_NAMESPACE
spec:
  gatewayClassName: KIC_NAME
  listeners:
  - name: proxy
    port: 80
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: demo-app
  namespace: APP_NAMESPACE
spec:
  parentRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: kong
    namespace: APP_NAMESPACE
  rules:
  - backendRefs:
    - kind: Service
      name: demo-app
      port: 5000
      weight: 1
    matches:
    - path:
        type: PathPrefix
        value: /
`
	ingress = strings.Replace(ingress, "KIC_NAME", kic, -1)
	ingress = strings.Replace(ingress, "APP_NAMESPACE", appNamespace, -1)
	return ingress
}

// requestFromGateway uses the builtin gateway pod as the client and requests an endpoint within the cluster
func requestFromGateway(gwAppName string, requestHost, requestPath string, responseChecker func(g Gomega, out string)) {
	// if requestHost is not provided, request the gateway instance itself
	if requestHost == "" {
		gatewaySvcIP, err1 := getServiceIP(cluster, TestNamespace, gwAppName)
		Expect(err1).ToNot(HaveOccurred())
		requestHost = gatewaySvcIP
	}

	gatewayPodName, err2 := PodNameOfApp(cluster, gwAppName, TestNamespace)
	Expect(err2).ToNot(HaveOccurred())

	Eventually(func(g Gomega) {
		// do not check for error, since wget return non-zero code on 403
		stdout, stderr, _ := cluster.Exec(TestNamespace,
			gatewayPodName,
			"kuma-gateway",
			"wget", "-q", "-O", "-", "-S", "-T", "3",
			requestHost+requestPath)
		responseChecker(g, stderr+stdout)
	}, "30s", "1s").Should(Succeed())
}

func getServiceIP(cluster *K8sCluster, namespace, svcName string) (string, error) {
	ip, err := retry.DoWithRetryInterfaceE(
		cluster.GetTesting(),
		fmt.Sprintf("get the clusterIP of Service %s in namespace %s", svcName, namespace),
		60,
		time.Second,
		func() (interface{}, error) {
			svc, err := k8s.GetServiceE(
				cluster.GetTesting(),
				cluster.GetKubectlOptions(namespace),
				svcName,
			)
			if err != nil || svc.Spec.ClusterIP == "" {
				return nil, errors.Wrapf(err, "could not get clusterIP")
			}

			return svc.Spec.ClusterIP, nil
		},
	)
	if err != nil {
		return "", err
	}

	return ip.(string), nil
}

func setupHelmRepo(t testing.TestingT) {
	repoName := strings.Split(Config.HelmChartName, "/")[0]

	// Adding the same repo multiple times is idempotent. The
	// `--force-update` flag prevents helm emitting an error
	// in this case.
	opts := helm.Options{}
	Expect(helm.RunHelmCommandAndGetOutputE(t, &opts,
		"repo", "add", "--force-update", repoName, Config.HelmRepoUrl)).Error().To(BeNil())

	Expect(helm.RunHelmCommandAndGetOutputE(t, &opts, "repo", "update")).Error().To(BeNil())
}
