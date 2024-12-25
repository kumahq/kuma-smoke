KUMACTLBIN = $(TOP)/build/$(SMOKE_PRODUCT_NAME)-$(SMOKE_PRODUCT_VERSION)/bin/kumactl

E2E_ENV_VARS += KUMA_K8S_TYPE=kind
E2E_ENV_VARS += TEST_ROOT="$(TOP)"
E2E_ENV_VARS += E2E_CONFIG_FILE="$(TOP)/pkg/smoke/cfg-$(SMOKE_PRODUCT_NAME).yaml"
E2E_ENV_VARS += KUMACTLBIN="$(KUMACTLBIN)"

INSTALLER_URL=https://kuma.io/installer.sh
ifeq ($(SMOKE_PRODUCT_NAME),kong-mesh)
	INSTALLER_URL = https://docs.konghq.com/mesh/installer.sh
endif

.PHONY: fetch-product
fetch-product:
	@mkdir -p build
	@[ -f $(KUMACTLBIN) ] || (cd build && echo "Downloading installer of $(SMOKE_PRODUCT_NAME) (version $(SMOKE_PRODUCT_VERSION))" && curl -L $(INSTALLER_URL) | VERSION=$(SMOKE_PRODUCT_VERSION) sh -)

.PHONY: deploy-kubernetes
deploy-kubernetes:
	@[ -f $(TOP)/build/kuma-smoke ] || (echo "Please run 'make build' first" && exit 1)
	@mkdir -p $(TOP)/build/kubernetes
	@$(TOP)/build/kuma-smoke kubernetes deploy --env-platform kind --kubeconfig-output $(TOP)/build/kubernetes/cluster.config

.PHONY: cleanup-kubernetes
cleanup-kubernetes:
	$(eval ENV_NAME=$(shell kubectl --kubeconfig=$(TOP)/build/kubernetes/cluster.config config view -o jsonpath='{.clusters[0].name}'))
	@$(TOP)/build/kuma-smoke kubernetes cleanup --env $(ENV_NAME)
	@rm -f $(TOP)/build/kubernetes/cluster.config

.PHONY: run
run: fetch-product deploy-kubernetes
	$(E2E_ENV_VARS) KUBECONFIG=$(TOP)/build/kubernetes/cluster.config $(GINKGO) -v --timeout=4h --json-report=raw-report.json ./pkg/smoke/...
	$(MAKE) cleanup-kubernetes
