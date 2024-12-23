package internal

import "time"

var (
	EnvironmentCreateTimeout = time.Minute * 10
	DefaultKumaVersion       = "2.9.2"
	DefaultKubernetesVersion = "1.31.1"

	SupportedProductNames = []string{"kuma", "kong-mesh"}
)
