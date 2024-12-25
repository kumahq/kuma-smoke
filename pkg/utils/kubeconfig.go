package utils

import (
	"github.com/spf13/cobra"
	"io"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func convertToClientCmdConfig(envName string, config *rest.Config) *clientcmdapi.Config {
	// caller should parse env name from the output (.clusters[0].cluster.name)
	return &clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			envName: {
				Server:                   config.Host,
				CertificateAuthorityData: config.CAData,
				TLSServerName:            config.ServerName,
				InsecureSkipTLSVerify:    config.Insecure,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			envName: {
				Cluster:  envName,
				AuthInfo: "default",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default": {
				ClientKeyData:         config.KeyData,
				ClientCertificateData: config.CertData,
				Token:                 config.BearerToken,
			},
		},
		CurrentContext: envName,
	}
}

func writeKubeconfigToFile(envName string, config *rest.Config, filename string) error {
	kubeconfig := convertToClientCmdConfig(envName, config)
	return clientcmd.WriteToFile(*kubeconfig, filename)
}

func writeKubeconfigToOutput(envName string, config *rest.Config, writer io.Writer) error {
	kubeconfig := convertToClientCmdConfig(envName, config)
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
