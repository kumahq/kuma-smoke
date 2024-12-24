SMOKE_PRODUCT_NAME ?= kuma
SMOKE_PRODUCT_VERSION ?= 2.9.2
CI_TOOLS_DIR ?= ${HOME}/.kuma-smoke-dev
CI_TOOLS_BIN_DIR=$(CI_TOOLS_DIR)/bin

GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)

GINKGO=$(CI_TOOLS_BIN_DIR)/ginkgo
YQ=$(CI_TOOLS_BIN_DIR)/yq

.PHONY: deps
deps:
	GOBIN=${CI_TOOLS_BIN_DIR} go install github.com/onsi/ginkgo/v2/ginkgo@$$(go list -f '{{.Version}}' -m github.com/onsi/ginkgo/v2)
	GOBIN=${CI_TOOLS_BIN_DIR} go install github.com/mikefarah/yq/v4@v4.13.0
