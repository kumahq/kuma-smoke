
.PHONY: build
build:
	@mkdir -p build
	@go build -o ./build/kuma-smoke ./cmd/.

.PHONY: clean
clean: cleanup-kubernetes
	@rm -rf build
	@rm -f raw-report.json
