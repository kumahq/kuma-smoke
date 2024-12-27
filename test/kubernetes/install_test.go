package kubernetes_test

import (
	"fmt"
	testclient "github.com/kumahq/kuma/test/framework/client"
	"github.com/kumahq/kuma/test/framework/deployments/testserver"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/kumahq/kuma/pkg/config/core"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func Install() {
	demoApp := "demo-app"
	demoGateway := "demo-app-gateway"

	BeforeAll(func() {
		err := NewClusterSetup().
			Install(Kuma(core.Zone, defaultKumaOptions()...)).
			Install(NamespaceWithSidecarInjection(TestNamespace)).
			Setup(cluster)
		Expect(err).ToNot(HaveOccurred())
	})

	E2EAfterAll(func() {
		Expect(cluster.TriggerDeleteNamespace(TestNamespace)).To(Succeed())
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

	It("should deploy the demo app", func() {
		By("install the demo app and wait for it to become ready")
		kumactl := NewKumactlOptionsE2E(cluster.GetTesting(), cluster.Name(), true)
		demoAppYAML, err := kumactl.RunKumactlAndGetOutput("install", "demo",
			"--namespace", TestNamespace,
			"--system-namespace", Config.KumaNamespace)

		Expect(err).ToNot(HaveOccurred())
		Expect(cluster.Install(YamlK8s(demoAppYAML))).To(Succeed())

		for _, fn := range []InstallFunc{
			WaitPodsAvailable(TestNamespace, demoApp),
			WaitPodsAvailable(TestNamespace, demoGateway)} {
			Expect(fn(cluster)).To(Succeed())
		}

		By("request the demo app from the gateway pod")
		Expect(cluster.Install(testserver.Install(
			testserver.WithName("test-client"),
			testserver.WithNamespace(TestNamespace),
		))).To(Succeed())
		Eventually(func(g Gomega) {
			stdout, stderr, err := testclient.CollectResponse(
				cluster,
				"test-client",
				"127.0.0.1/version",
				testclient.FromKubernetesPod(TestNamespace, demoGateway),
				testclient.WithVerbose(),
			)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(stdout + stderr).To(ContainSubstring("200 OK"))
			g.Expect(stdout + stderr).To(ContainSubstring("server: Kuma Gateway"))
		}, "30s", "1s").Should(Succeed())
	})

	It("should distribute certs when mTLS is enabled", func() {
		By("enable mTLS on the cluster")
		Expect(cluster.Install(MTLSMeshKubernetes("default"))).To(Succeed())

		Eventually(func(g Gomega) {
			out, err := k8s.RunKubectlAndGetOutputE(
				cluster.GetTesting(),
				cluster.GetKubectlOptions(),
				"get", "meshinsights", "default", "-ojsonpath='{.spec.mTLS.issuedBackends.ca-1.total}'",
			)

			g.Expect(err).ToNot(HaveOccurred())
			number, err := strconv.Atoi(out)
			g.Expect(err).ToNot(HaveOccurred())
			Expect(number).To(BeNumerically(">", 0))
		}, "60s", "1s").Should(Succeed())

		By("the demo-app should not be requested without a MeshTrafficPermission applied")
		Eventually(func(g Gomega) {
			stdout, stderr, err := testclient.CollectResponse(
				cluster,
				"test-client",
				"127.0.0.1/version",
				testclient.FromKubernetesPod(TestNamespace, demoGateway),
				testclient.WithVerbose(),
			)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(stdout + stderr).To(ContainSubstring("403 Forbidden"))
			g.Expect(stdout + stderr).To(ContainSubstring("RBAC: access denied"))
		}, "30s", "1s").Should(Succeed())

		By("the demo-app should be requested successfully with a MeshTrafficPermission applied")
		mtp := `
apiVersion: kuma.io/v1alpha1
kind: MeshTrafficPermission
metadata:
  name: allow-any
  namespace: kong-mesh-system
spec:
  targetRef:
    kind: Mesh
  from:
    - targetRef:
        kind: Mesh
      default:
        action: Allow`
		Expect(cluster.Install(YamlK8s(mtp))).To(Succeed())

		Eventually(func(g Gomega) {
			stdout, stderr, err := testclient.CollectResponse(
				cluster,
				"test-client",
				"127.0.0.1/version",
				testclient.FromKubernetesPod(TestNamespace, demoGateway),
				testclient.WithVerbose(),
			)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(stdout + stderr).To(ContainSubstring("200 OK"))
			g.Expect(stdout + stderr).To(ContainSubstring("server: Kuma Gateway"))
		}, "30s", "1s").Should(Succeed())
	})

	It("should run stable", func() {
		time.Sleep(10 * time.Second)

		Expect(CpRestarted(cluster)).To(BeFalse(), cluster.Name()+" restarted in this suite, this should not happen.")

		logOutputFile := filepath.Join(Config.DebugDir, fmt.Sprintf("%s-logs.log", Config.KumaServiceName))
		logs, err := cluster.GetKumaCPLogs()

		Expect(err).To(Not(HaveOccurred()))
		Expect(os.WriteFile(logOutputFile, []byte(logs), 0o600)).To(Succeed())
	})
}
