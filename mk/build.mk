
.PHONY: build
build:
	mkdir -p build
	go build -o ./build/kuma-smoke ./cmd/.