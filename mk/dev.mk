SMOKE_PRODUCT_NAME ?= kuma
SMOKE_PRODUCT_VERSION ?= 2.9.2
CI_TOOLS_DIR ?= ${HOME}/.kuma-smoke-dev
CI_TOOLS_BIN_DIR=$(CI_TOOLS_DIR)/bin

export PATH := $(CI_TOOLS_BIN_DIR):$(PATH)

TOOLS_DIR = $(TOP)/tools
TOOLS_DEPS_DIRS=$(TOP)/mk/dependencies
TOOLS_DEPS_LOCK_FILE=mk/dependencies/deps.lock
TOOLS_MAKEFILE=$(TOP)/mk/dev.mk
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

GINKGO=$(CI_TOOLS_BIN_DIR)/ginkgo

.PHONY: dev/go
dev/go:
	GOBIN=${CI_TOOLS_BIN_DIR} go install github.com/onsi/ginkgo/v2/ginkgo@$$(go list -f '{{.Version}}' -m github.com/onsi/ginkgo/v2)
	GOBIN=${CI_TOOLS_BIN_DIR} go install github.com/mikefarah/yq/v4@v4.13.0

.PHONY: dev/tools
dev/tools: dev/go
	$(TOOLS_DIR)/dev/install-dev-tools.sh $(CI_TOOLS_BIN_DIR) $(CI_TOOLS_DIR) "$(TOOLS_DEPS_DIRS)" $(TOOLS_DEPS_LOCK_FILE) $(GOOS) $(GOARCH) $(TOOLS_MAKEFILE)


.PHONY: kind
kind:
	echo $(PATH)
	kind get clusters
