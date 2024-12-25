package cluster_providers

import (
	"context"
	"fmt"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/spf13/cobra"
)

type ClusterProvider interface {
	ClusterProvider(cmd *cobra.Command, envName string) (clusters.Builder, error)
	NewFromExisting(ctx context.Context, cmd *cobra.Command, envName string) (clusters.Cluster, error)
}

var supportedClusterProviders = map[string]ClusterProvider{}
var SupportedProviderNames []string // , "kind", "gke", "aks", "eks", "k3d"

func Register(name string, provider ClusterProvider) {
	supportedClusterProviders[name] = provider
	SupportedProviderNames = append(SupportedProviderNames, name)
}

func GetBuilder(providerName string, cmd *cobra.Command, envName string) (clusters.Builder, error) {
	if provider, ok := supportedClusterProviders[providerName]; ok {
		return provider.ClusterProvider(cmd, envName)
	}

	return nil, fmt.Errorf("environment platform not supported: %s", providerName)
}

func NewClusterFromExisting(providerName string, ctx context.Context, cmd *cobra.Command, envName string) (clusters.Cluster, error) {
	if provider, ok := supportedClusterProviders[providerName]; ok {
		return provider.NewFromExisting(ctx, cmd, envName)
	}

	return nil, fmt.Errorf("environment platform not supported: %s", providerName)
}
