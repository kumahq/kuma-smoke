package eks

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kris-nova/logger"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	err_pkg "github.com/pkg/errors"
	"github.com/spf13/cobra"
	"io"
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
	err := guardOnEnv()
	if err != nil {
		return nil, err
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err_pkg.Wrap(err, "failed to load AWS SDK config")
	}
	return InitFromExisting(ctx, cfg, envName)
}

func init() {
	// By default, we don't log anything (until KTF support a logging mechanism)
	logger.Writer = io.Discard
	cluster_providers.Register("eks", eksProvider{})
}
