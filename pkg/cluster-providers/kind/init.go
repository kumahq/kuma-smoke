package gke

import (
	"context"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/kind"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	"github.com/spf13/cobra"
)

type kindProvider struct{}

func (kindProvider) ClusterProvider(_ *cobra.Command, _ string) (clusters.Builder, error) {
	// kind builtin supported by KTF, so don't need to do anything here
	return nil, nil
}
func (kindProvider) NewFromExisting(_ context.Context, _ *cobra.Command, envName string) (clusters.Cluster, error) {
	return kind.NewFromExisting(envName)
}

func init() {
	cluster_providers.Register("kind", kindProvider{})
}
