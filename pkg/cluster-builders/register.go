package cluster_builders

import (
	"errors"
	"fmt"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/spf13/cobra"
)

type BuilderFactory func(cmd *cobra.Command, envName string) (error, clusters.Builder)

var supportedClusterBuilders = map[string]BuilderFactory{}
var SupportedBuilderNames = []string{"kind"} // , "gke", "aks", "eks", "k3d"

func Register(name string, fac BuilderFactory) {
	supportedClusterBuilders[name] = fac
	SupportedBuilderNames = append(SupportedBuilderNames, name)
}

func GetRegisteredBuilder(clusterBuilderName string, cmd *cobra.Command, envName string) (error, clusters.Builder) {
	if cb, ok := supportedClusterBuilders[clusterBuilderName]; ok {
		return cb(cmd, envName)
	}

	if clusterBuilderName == "kind" {
		return nil, nil
	}

	return errors.New(fmt.Sprintf("environment platform not supported: %s", clusterBuilderName)), nil
}
