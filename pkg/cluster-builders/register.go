package cluster_builders

import "github.com/kong/kubernetes-testing-framework/pkg/clusters"

var supportedClusterBuilders = map[string]clusters.Builder{}
var SupportedBuilderNames = []string{"kind"} // , "k3d", "gcp-standard", "azure-aks", "aws-eks"

func Register(name string, b clusters.Builder) {
	supportedClusterBuilders[name] = b
	SupportedBuilderNames = append(SupportedBuilderNames, name)
}

func GetRegisteredBuilder(name string) clusters.Builder {
	if cb, ok := supportedClusterBuilders[name]; ok {
		return cb
	}
	return nil
}
