package utils

import (
	"github.com/kong/kubernetes-testing-framework/pkg/utils/kubernetes/generators"
	"github.com/spf13/cobra"
	"io"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func writeKubeconfigToFile(envName string, config *rest.Config, filename string) error {
	kubeconfig := generators.NewClientConfigForRestConfig(envName, config)
	return clientcmd.WriteToFile(*kubeconfig, filename)
}

func writeKubeconfigToOutput(envName string, config *rest.Config, writer io.Writer) error {
	kubeconfig := generators.NewClientConfigForRestConfig(envName, config)
	content, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		return err
	}

	_, err = writer.Write(content)
	return err
}

func WriteKubeconfig(envName string, cmd *cobra.Command, config *rest.Config, output string) error {
	if output == "-" {
		return writeKubeconfigToOutput(envName, config, cmd.OutOrStdout())
	} else {
		return writeKubeconfigToFile(envName, config, output)
	}
}
