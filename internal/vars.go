package internal

import "time"

var (
	EnvironmentCreateTimeout = time.Minute * 10
	DefaultKumaVersion       = "2.9.2"
	DefaultKubernetesVersion = "1.25.16" // referenced from https://github.com/kumahq/kuma/blob/2.9.2/mk/dev.mk#L24-L25

	SupportedProductNames = []string{"kuma", "kong-mesh"}
)
