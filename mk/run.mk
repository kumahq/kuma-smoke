KUMACTLBIN = $(TOP)/build/$(SMOKE_PRODUCT_NAME)-$(SMOKE_PRODUCT_VERSION)/bin/kumactl

E2E_ENV_VARS += K8SCLUSTERS="kuma-smoke"
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
	mkdir -p build
	[ -f $(KUMACTLBIN) ] || (cd build && curl -L $(INSTALLER_URL) | VERSION=$(SMOKE_PRODUCT_VERSION) sh -)

.PHONY: deploy-k8s
deploy-k8s:
	# caller should parse env name from the output (.clusters[0].cluster.name)
	# set SMOKE_PRODUCT_VERSION=0.0.0 to skip deploying the product (so that only the cluster is created)
	./build/kuma-smoke kubernetes deploy --env-platform kind --product $(SMOKE_PRODUCT_NAME) --version $(SMOKE_PRODUCT_VERSION)

.PHONY: cleanup-k8s
cleanup-k8s:
	# caller should parse env name from the output (.clusters[0].cluster.name)
	# ./build/kuma-smoke kubernetes cleanup --env
	# remove the kubeconfig files

.PHONY: run
run: fetch-product deploy-k8s
	$(E2E_ENV_VARS) $(GINKGO) -v --timeout=4h --json-report=raw-report.json ./pkg/smoke/...
	$(MAKE) cleanup-k8s
