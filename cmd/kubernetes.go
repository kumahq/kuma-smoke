package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/blang/semver/v4"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters/addons/metallb"
	"github.com/kong/kubernetes-testing-framework/pkg/environments"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/gke"
	_ "github.com/kumahq/kuma-smoke/pkg/cluster-providers/kind"
	"github.com/kumahq/kuma-smoke/pkg/utils"
	"github.com/kumahq/kuma-smoke/test"
	"github.com/spf13/cobra"
	"slices"
	"strings"
)

type deployOptions struct {
	k8sVersionOptions
	envOptions
	kubeconfigOptions
}

var k8sDeployOpt = deployOptions{}
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
			utils.CmdStdErr(cmd, "Warning: deploying a Kubernetes cluster older than the minimal supported version by Kuma. "+
				"The minimal supported version by Kuma is %s\n", test.MinSupportedKubernetesVer)
		}

		err = validatePlatformName(k8sDeployOpt.envPlatform)
		cobra.CheckErr(err)

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), utils.EnvironmentCreateTimeout)
		defer cancel()

		envBuilder := environments.NewBuilder()
		randomName := strings.Replace(envBuilder.Name, "-", "", -1)
		envBuilder = envBuilder.WithName("kuma-smoke-" + randomName[len(randomName)-10:])

		clsBuilder, err := cluster_providers.GetBuilder(k8sDeployOpt.envPlatform, cmd, envBuilder.Name)
		cobra.CheckErr(err)
		if clsBuilder != nil {
			envBuilder = envBuilder.WithClusterBuilder(clsBuilder)
		} else {
			envBuilder = envBuilder.WithKubernetesVersion(k8sDeployOpt.parsedK8sVersion)
		}
		if k8sDeployOpt.envPlatform == "kind" {
			envBuilder = envBuilder.WithAddons(metallb.New())
		}

		utils.CmdStdErr(cmd, "building new environment %s\n", envBuilder.Name)
		env, err := envBuilder.Build(ctx)
		cobra.CheckErr(err)

		addons := env.Cluster().ListAddons()
		for _, addon := range addons {
			utils.CmdStdErr(cmd, "waiting for addon %s to become ready...\n", addon.Name())
		}

		utils.CmdStdErr(cmd, "waiting for environment to become ready (this can take some time)...\n")
		cobra.CheckErr(<-env.WaitForReady(ctx))

		utils.CmdStdErr(cmd, "environment %s was created successfully!\n", env.Name())

		if k8sDeployOpt.kubeconfigOutputFile != "" {
			cobra.CheckErr(utils.WriteKubeconfig(envBuilder.Name, cmd, env.Cluster().Config(), k8sDeployOpt.kubeconfigOutputFile))
		} else {
			utils.CmdStdout(cmd, "%s", env.Name())
		}
		return nil
	},
}

type exportKubeconfigOptions struct {
	envOptions
	kubeconfigOptions
}

var k8sExportKubeConfigOpt = exportKubeconfigOptions{}
var k8sExportKubeConfigCmd = &cobra.Command{
	Use:   "export-kubeconfig",
	Short: "export kubeconfig for a created cluster",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		err := validatePlatformName(k8sExportKubeConfigOpt.envPlatform)
		cobra.CheckErr(err)
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), utils.EnvironmentCreateTimeout)
		defer cancel()

		_, err := cluster_providers.GetBuilder(k8sExportKubeConfigOpt.envPlatform, cmd, k8sExportKubeConfigOpt.envName)
		cobra.CheckErr(err)

		existingCls, err := cluster_providers.NewClusterFromExisting(k8sExportKubeConfigOpt.envPlatform, ctx, cmd, k8sExportKubeConfigOpt.envName)
		cobra.CheckErr(err)

		cobra.CheckErr(utils.WriteKubeconfig(k8sExportKubeConfigOpt.envName, cmd, existingCls.Config(), k8sExportKubeConfigOpt.kubeconfigOutputFile))
		return nil
	},
}

var k8sCleanupOpt = envOptions{}
var k8sCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "cleanup the installed resources during the smoke tests",
	PreRunE: func(cmd *cobra.Command, args []string) error {
		err := validatePlatformName(k8sCleanupOpt.envPlatform)
		cobra.CheckErr(err)

		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(context.Background(), utils.CleanupTimeout)
		defer cancel()

		_, err := cluster_providers.GetBuilder(k8sCleanupOpt.envPlatform, cmd, k8sCleanupOpt.envName)
		cobra.CheckErr(err)

		existingCls, err := cluster_providers.NewClusterFromExisting(k8sCleanupOpt.envPlatform, ctx, cmd, k8sCleanupOpt.envName)
		cobra.CheckErr(err)

		utils.CmdStdErr(cmd, "cleaning up cluster of environment %s\n", k8sCleanupOpt.envName)
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

func init() {
	k8sDeployCmd.Flags().StringVar(&k8sDeployOpt.kubernetesVersion, "kubernetes-version", test.MaxSupportedKubernetesVer, "The version of Kubernetes to deploy")
	k8sDeployCmd.Flags().StringVar(&k8sDeployOpt.kubeconfigOutputFile, "kubeconfig-output", "", "The file path used to write the generated kubeconfig")
	k8sDeployCmd.Flags().StringVar(&k8sDeployOpt.envPlatform, "env-platform", "kind",
		fmt.Sprintf("The platform to deploy the environment on (%s)",
			strings.Join(cluster_providers.SupportedProviderNames, ",")))
	k8sCmd.AddCommand(k8sDeployCmd)

	k8sExportKubeConfigCmd.Flags().StringVar(&k8sExportKubeConfigOpt.envName, "env", "", "name of the existing environment")
	_ = k8sExportKubeConfigCmd.MarkFlagRequired("env")
	k8sExportKubeConfigCmd.Flags().StringVar(&k8sExportKubeConfigOpt.envPlatform, "env-platform", "kind",
		fmt.Sprintf("The platform that the environment was deployed on (%s)",
			strings.Join(cluster_providers.SupportedProviderNames, ",")))
	k8sExportKubeConfigCmd.Flags().StringVar(&k8sExportKubeConfigOpt.kubeconfigOutputFile, "kubeconfig-output", "", "The file path used to write the generated kubeconfig")
	_ = k8sExportKubeConfigCmd.MarkFlagRequired("kubeconfig-output")
	k8sCmd.AddCommand(k8sExportKubeConfigCmd)

	k8sCleanupCmd.Flags().StringVar(&k8sCleanupOpt.envName, "env", "", "name of the existing environment")
	_ = k8sCleanupCmd.MarkFlagRequired("env")
	k8sCleanupCmd.Flags().StringVar(&k8sCleanupOpt.envPlatform, "env-platform", "kind",
		fmt.Sprintf("The platform that the environment was deployed on (%s)",
			strings.Join(cluster_providers.SupportedProviderNames, ",")))
	k8sCmd.AddCommand(k8sCleanupCmd)
}
