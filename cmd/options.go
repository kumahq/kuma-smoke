package main

import "github.com/blang/semver/v4"

type k8sVersionOptions struct {
	kubernetesVersion string
	parsedK8sVersion  semver.Version
}

type envOptions struct {
	envName     string
	envPlatform string
}

type kubeconfigOptions struct {
	kubeconfigOutputFile string
}
