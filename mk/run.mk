SMOKE_PRODUCT_NAME ?= kuma
SMOKE_PRODUCT_VERSION ?= 2.9.2
SMOKE_ENV_TYPE ?= kind

# Extract major, minor, and patch versions
MAJOR := $(word 1,$(subst ., ,$(SMOKE_PRODUCT_VERSION)))
MINOR := $(word 2,$(subst ., ,$(SMOKE_PRODUCT_VERSION)))
PATCH := $(word 3,$(subst ., ,$(SMOKE_PRODUCT_VERSION)))

PREV_MINOR := $(if $(filter-out 1,$(MAJOR)),$(shell echo $$(($(MINOR)-1))),$(MINOR))
SMOKE_PRODUCT_VERSION_PREV_MINOR := $(MAJOR).$(PREV_MINOR).0
KUMACTLBIN_PREV_MINOR = $(TOP)/build/$(SMOKE_PRODUCT_NAME)-$(SMOKE_PRODUCT_VERSION_PREV_MINOR)/bin/kumactl

PREV_PATCH := $(if $(filter-out 0,$(PATCH)),$(shell echo $$(($(PATCH)-1))),0)
SMOKE_PRODUCT_VERSION_PREV_PATCH := $(MAJOR).$(MINOR).$(PREV_PATCH)
KUMACTLBIN_PREV_PATCH = $(TOP)/build/$(SMOKE_PRODUCT_NAME)-$(SMOKE_PRODUCT_VERSION_PREV_PATCH)/bin/kumactl

KUMACTLBIN = $(TOP)/build/$(SMOKE_PRODUCT_NAME)-$(SMOKE_PRODUCT_VERSION)/bin/kumactl

E2E_ENV_VARS += KUMA_K8S_TYPE=kind
E2E_ENV_VARS += TEST_ROOT="$(TOP)"
E2E_ENV_VARS += E2E_CONFIG_FILE="$(TOP)/test/cfg-$(SMOKE_PRODUCT_NAME).yaml"
E2E_ENV_VARS += KUMA_DEBUG_DIR="$(TOP)/build/debug-output"
E2E_ENV_VARS += KUMACTLBIN="$(KUMACTLBIN)"
E2E_ENV_VARS += KUMA_GLOBAL_IMAGE_TAG="$(SMOKE_PRODUCT_VERSION)"
E2E_ENV_VARS += KUMACTLBIN_PREV_MINOR="$(KUMACTLBIN_PREV_MINOR)"
E2E_ENV_VARS += SMOKE_PRODUCT_VERSION_PREV_MINOR="$(SMOKE_PRODUCT_VERSION_PREV_MINOR)"
E2E_ENV_VARS += KUMACTLBIN_PREV_PATCH="$(KUMACTLBIN_PREV_PATCH)"
E2E_ENV_VARS += SMOKE_PRODUCT_VERSION_PREV_PATCH="$(SMOKE_PRODUCT_VERSION_PREV_PATCH)"

INSTALLER_URL=https://kuma.io/installer.sh
ifeq ($(SMOKE_PRODUCT_NAME),kong-mesh)
	INSTALLER_URL = https://docs.konghq.com/mesh/installer.sh
endif

.PHONY: fetch-product
fetch-product:
	@mkdir -p build
	@[ -f $(KUMACTLBIN) ] || (cd build && echo "Downloading installer of $(SMOKE_PRODUCT_NAME) (version $(SMOKE_PRODUCT_VERSION))" && curl -L $(INSTALLER_URL) | VERSION=$(SMOKE_PRODUCT_VERSION) sh -)
	@[ -f $(KUMACTLBIN_PREV_MINOR) ] || (cd build && echo "Downloading installer of $(SMOKE_PRODUCT_NAME) (version $(SMOKE_PRODUCT_VERSION_PREV_MINOR))" && curl -L $(INSTALLER_URL) | VERSION=$(SMOKE_PRODUCT_VERSION_PREV_MINOR) sh -)
	@[ -f $(KUMACTLBIN_PREV_PATCH) ] || (cd build && echo "Downloading installer of $(SMOKE_PRODUCT_NAME) (version $(SMOKE_PRODUCT_VERSION_PREV_PATCH))" && curl -L $(INSTALLER_URL) | VERSION=$(SMOKE_PRODUCT_VERSION_PREV_PATCH) sh -)

.PHONY: deploy-kubernetes
deploy-kubernetes:
	@[ -f $(TOP)/build/kuma-smoke ] || (echo "Please run 'make build' first" && exit 1)
	@mkdir -p $(TOP)/build/kubernetes
	@$(TOP)/build/kuma-smoke kubernetes deploy --env-platform $(SMOKE_ENV_TYPE) --kubeconfig-output $(TOP)/build/kubernetes/cluster.config

.PHONY: cleanup-kubernetes
cleanup-kubernetes:
	$(eval ENV_NAME=$(shell kubectl --kubeconfig=$(TOP)/build/kubernetes/cluster.config config view -o jsonpath='{.clusters[0].name}'))
	@$(TOP)/build/kuma-smoke kubernetes cleanup --env $(ENV_NAME)
	@rm -f $(TOP)/build/kubernetes/cluster.config

.PHONY: run
run: fetch-product deploy-kubernetes
	mkdir -p $(TOP)/build/debug-output
	$(E2E_ENV_VARS) KUBECONFIG=$(TOP)/build/kubernetes/cluster.config $(GINKGO) -v --timeout=4h --json-report=raw-report.json ./test/...
	$(MAKE) cleanup-kubernetes
