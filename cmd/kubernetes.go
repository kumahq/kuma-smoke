package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/blang/semver/v4"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/addons/metallb"
	"github.com/kong/kubernetes-testing-framework/pkg/environments"
	"github.com/kumahq/kuma-smoke/internal"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/gke"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/kind"
	"github.com/kumahq/kuma-smoke/test"
	"github.com/spf13/cobra"
	"slices"
	"strings"
)

var k8sDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "deploy the cluster and product that the smoke tests will be running on",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		var err error
		k8sDeployOpt.parsedK8sVersion, err = semver.Parse(strings.TrimPrefix(k8sDeployOpt.kubernetesVersion, "v"))
		cobra.CheckErr(err)

		kumaMinSupported := semver.MustParse(test.MinSupportedKubernetesVer)
		if k8sDeployOpt.parsedK8sVersion.Major < kumaMinSupported.Major ||
			(k8sDeployOpt.parsedK8sVersion.Major == kumaMinSupported.Major && k8sDeployOpt.parsedK8sVersion.Minor < kumaMinSupported.Minor) {
			internal.CmdStdErr(cmd, "Warning: deploying a Kubernetes cluster older than the minimal supported version by Kuma. "+
				"The minimal supported version by Kuma is %s\n", test.MinSupportedKubernetesVer)
		}

		err = validatePlatformName(envPlatform)
		cobra.CheckErr(err)

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), internal.EnvironmentCreateTimeout)
		defer cancel()

		envBuilder := environments.NewBuilder()
		randomName := strings.Replace(envBuilder.Name, "-", "", -1)
		envBuilder = envBuilder.WithName("kuma-smoke-" + randomName[len(randomName)-10:])

		clsBuilder, err := cluster_providers.GetBuilder(envPlatform, cmd, envBuilder.Name)
		cobra.CheckErr(err)
		if clsBuilder != nil {
			envBuilder = envBuilder.WithClusterBuilder(clsBuilder)
		} else {
			envBuilder = envBuilder.WithKubernetesVersion(k8sDeployOpt.parsedK8sVersion)
		}
		envBuilder = configureAddons(envBuilder)

		internal.CmdStdErr(cmd, "building new environment %s\n", envBuilder.Name)
		env, err := envBuilder.Build(ctx)
		cobra.CheckErr(err)

		addons := env.Cluster().ListAddons()
		for _, addon := range addons {
			internal.CmdStdErr(cmd, "waiting for addon %s to become ready...\n", addon.Name())
		}

		internal.CmdStdErr(cmd, "waiting for environment to become ready (this can take some time)...\n")
		cobra.CheckErr(<-env.WaitForReady(ctx))

		internal.CmdStdErr(cmd, "environment %s was created successfully!\n", env.Name())

		cobra.CheckErr(internal.WriteKubeconfig(envBuilder.Name, cmd, env.Cluster().Config(), k8sDeployOpt.kubeconfigOutputFile))
		return nil
	},
}

func configureAddons(builder *environments.Builder) *environments.Builder {
	if envPlatform == "kind" {
		builder = builder.WithAddons(metallb.New())
	}

	return builder
}

var k8sCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "cleanup the installed resources during the smoke tests",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		err := validatePlatformName(envPlatform)
		cobra.CheckErr(err)

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), internal.CleanupTimeout)
		defer cancel()

		_, err := cluster_providers.GetBuilder(envPlatform, cmd, envName)
		cobra.CheckErr(err)

		existingCls, err := cluster_providers.NewClusterFromExisting(envPlatform, ctx, cmd, envName)
		cobra.CheckErr(err)

		internal.CmdStdErr(cmd, "cleaning up cluster of environment %s\n", envName)
		err = existingCls.Cleanup(ctx)
		cobra.CheckErr(err)

		return nil
	},
}

func validatePlatformName(platform string) error {
	if !slices.Contains(cluster_providers.SupportedProviderNames, platform) {
		return fmt.Errorf("unsupported platform: '%s'. supported platforms are: %s",
			platform, strings.Join(cluster_providers.SupportedProviderNames, ", "))
	}
	return nil
}

var k8sCmd = &cobra.Command{
	Use:   "kubernetes",
	Short: "Prepare and run smoke tests for Kuma on Kubernetes",
	RunE: func(cmd *cobra.Command, args []string) error {
		return errors.New("must pass a subcommand")
	},
}

type deployOptions struct {
	kubernetesVersion    string
	kubeconfigOutputFile string
	parsedK8sVersion     semver.Version
}

var k8sDeployOpt = deployOptions{}
var envName = ""
var envPlatform = ""

func init() {
	k8sDeployCmd.Flags().StringVar(&k8sDeployOpt.kubernetesVersion, "kubernetes-version", test.MaxSupportedKubernetesVer, "The version of Kubernetes to deploy")
	k8sDeployCmd.Flags().StringVar(&k8sDeployOpt.kubeconfigOutputFile, "kubeconfig-output", "", "The file path used to write the generated kubeconfig")
	_ = k8sDeployCmd.MarkFlagRequired("kubeconfig-output")
	k8sDeployCmd.Flags().StringVar(&envPlatform, "env-platform", "kind",
		fmt.Sprintf("The platform to deploy the environment on (%s)",
			strings.Join(cluster_providers.SupportedProviderNames, ",")))
	k8sCmd.AddCommand(k8sDeployCmd)

	k8sCleanupCmd.Flags().StringVar(&envName, "env", "", "name of the existing environment")
	k8sCleanupCmd.Flags().StringVar(&envPlatform, "env-platform", "kind",
		fmt.Sprintf("The platform that the environment was deployed on (%s)",
			strings.Join(cluster_providers.SupportedProviderNames, ",")))
	k8sCmd.AddCommand(k8sCleanupCmd)
}
