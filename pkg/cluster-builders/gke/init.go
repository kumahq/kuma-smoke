package gke

import (
	"errors"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/types/gke"
	cluster_builders "github.com/kumahq/kuma-smoke/pkg/cluster-builders"
	"github.com/spf13/cobra"
	"os"
)

func init() {
	cluster_builders.Register("gke", func(cmd *cobra.Command, envName string) (error, clusters.Builder) {
		// todo: print help information to make these env vars more discoverable
		gkeJsonCreds, ok := os.LookupEnv("GKE_JSON_CREDENTIALS")
		if !ok {
			return errors.New("GKE_JSON_CREDENTIALS is not set"), nil
		}
		gkeProject, ok := os.LookupEnv("GKE_PROJECT")
		if !ok {
			return errors.New("GKE_PROJECT is not set"), nil
		}
		gkeLocation, ok := os.LookupEnv("GKE_LOCATION")
		if !ok {
			return errors.New("GKE_LOCATION is not set"), nil
		}

		gkeBuilder := gke.NewBuilder([]byte(gkeJsonCreds), gkeProject, gkeLocation)
		gkeBuilder.Name = envName
		return nil, gkeBuilder
	})
}
