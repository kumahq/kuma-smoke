package kubernetes_test

import (
	"fmt"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/kumahq/kuma/pkg/config/core"
	. "github.com/kumahq/kuma/test/framework"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func Install() {
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

	It("should deploy the mesh", func() {
		// just to see stabilized stats before we go further
		Expect(true).To(BeTrue())
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

	It("should distribute certs when mTLS is enabled", func() {
		Expect(cluster.Install(MTLSMeshKubernetes("default"))).To(Succeed())

		propagationStart := time.Now()
		Eventually(func(g Gomega) {
			_, err := k8s.RunKubectlAndGetOutputE(
				cluster.GetTesting(),
				cluster.GetKubectlOptions(),
				"get", "meshinsights", "default", "-ojsonpath='{.spec.mTLS.issuedBackends.ca-1.total}'",
			)
			g.Expect(err).ToNot(HaveOccurred())
			//g.Expect(out).To(Equal(fmt.Sprintf("'%d'", expectedCerts)))
		}, "60s", "1s").Should(Succeed())
		AddReportEntry("certs_propagation_duration", time.Since(propagationStart).Milliseconds())
	})
}
