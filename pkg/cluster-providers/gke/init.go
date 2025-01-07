package gke

import (
	"context"
	"errors"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/gke"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	"github.com/spf13/cobra"
	"os"
)

type gkeProvider struct{}

func (gkeProvider) ClusterProvider(_ *cobra.Command, envName string) (clusters.Builder, error) {
	// todo: print help information to make these env vars more discoverable
	gkeJsonCreds := os.Getenv(gke.GKECredsVar)
	if gkeJsonCreds == "" {
		return nil, errors.New(gke.GKECredsVar + " is not set")
	}
	gkeProject := os.Getenv(gke.GKEProjectVar)
	if gkeProject == "" {
		return nil, errors.New(gke.GKEProjectVar + " is not set")
	}
	gkeLocation := os.Getenv(gke.GKELocationVar)
	if gkeLocation == "" {
		return nil, errors.New(gke.GKELocationVar + " is not set")
	}

	gkeBuilder := gke.NewBuilder([]byte(gkeJsonCreds), gkeProject, gkeLocation)
	gkeBuilder.Name = envName
	gkeBuilder.WithNodeMachineType("e2-standard-16")

	return gkeBuilder, nil
}

func (gkeProvider) NewFromExisting(ctx context.Context, _ *cobra.Command, envName string) (clusters.Cluster, error) {
	return gke.NewFromExistingWithEnv(ctx, envName)
}

func init() {
	cluster_providers.Register("gke", gkeProvider{})
}
