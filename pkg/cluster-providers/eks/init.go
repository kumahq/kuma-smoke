package eks

import (
	"context"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	"github.com/spf13/cobra"
)

const (
	envAccessKeyId               = "AWS_ACCESS_KEY_ID"
	envAccessKey                 = "AWS_SECRET_ACCESS_KEY"
	envRegion                    = "AWS_REGION"
	eksClusterType clusters.Type = "eks"
)

type eksProvider struct{}

func (eksProvider) ClusterProvider(_ *cobra.Command, envName string) (clusters.Builder, error) {
	err := guardOnEnv()
	if err != nil {
		return nil, err
	}

	eksBuilder := NewBuilder()
	eksBuilder.Name = envName

	return eksBuilder, nil
}

func (eksProvider) NewFromExisting(ctx context.Context, _ *cobra.Command, envName string) (clusters.Cluster, error) {
	return NewFromExisting(ctx, envName)
}

func init() {
	cluster_providers.Register("eks", eksProvider{})
}
