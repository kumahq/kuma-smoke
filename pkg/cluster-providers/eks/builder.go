package eks

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/kumahq/kuma-smoke/pkg/cluster-providers/eks/aws-operations"
	"github.com/pkg/errors"
	"github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	eksctlapi "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"os"
	"strings"
)

// Builder generates clusters.Cluster objects backed by GKE given
// provided configuration options.DeleteVPC
type Builder struct {
	Name            string
	waitForTeardown bool

	addons          clusters.Addons
	clusterVersion  *semver.Version
	nodeMachineType string
}

const (
	defaultNodeMachineType   = "c5.4xlarge"
	defaultKubernetesVersion = "1.31.1"
	envKeyNodeSSHKeyName     = "EKS_NODE_SSH_KEY"
)

// NewBuilder provides a new *Builder object.
func NewBuilder() *Builder {
	k8sVer := semver.MustParse(defaultKubernetesVersion)
	return &Builder{
		Name:            fmt.Sprintf("t-%s", uuid.NewString()),
		nodeMachineType: defaultNodeMachineType,
		addons:          make(clusters.Addons),
		clusterVersion:  &k8sVer,
	}
}

// WithName indicates a custom name to use for the cluster.
func (b *Builder) WithName(name string) *Builder {
	b.Name = name
	return b
}

// WithClusterVersion configures the Kubernetes cluster version for the Builder
// to use when building the GKE cluster.
func (b *Builder) WithClusterVersion(version semver.Version) *Builder {
	b.clusterVersion = &version
	return b
}

func (b *Builder) WithNodeMachineType(machineType string) *Builder {
	b.nodeMachineType = machineType
	return b
}

// WithWaitForTeardown sets a flag telling whether the cluster should wait for
// a cleanup operation synchronously.
//
// Default: `false`.
func (b *Builder) WithWaitForTeardown(wait bool) *Builder {
	b.waitForTeardown = wait
	return b
}

// Build creates and configures clients for a GKE-based Kubernetes clusters.Cluster.
func (b *Builder) Build(ctx context.Context) (clusters.Cluster, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load AWS SDK config")
	}

	ec2Client := ec2.NewFromConfig(cfg)
	eksClient := eks.NewFromConfig(cfg)
	iamClient := iam.NewFromConfig(cfg)

	version := minorVersion(b.clusterVersion)
	clusterRoleArn, nodeRoleArn, err := aws_operations.CreateRoles(ctx, iamClient, b.Name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create IAM roles")
	}
	subnetAvZones, err := aws_operations.GetAvailabilityZones(ctx, ec2Client, cfg.Region)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get availability zones in region %s", cfg.Region)
	}

	vpcId, subnetIDs, err := aws_operations.CreateVPC(ctx, ec2Client, subnetAvZones)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create VPC")
	}
	clusterName := b.Name

	cpSgId, err := aws_operations.CreateControlPlaneSecurityGroup(ctx, ec2Client, vpcId, clusterName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create control plane security group in VPC %s", vpcId)
	}

	_, err = aws_operations.CreateCluster(ctx, eksClient, clusterName, clusterRoleArn, version, cpSgId, subnetIDs)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS cluster %s", clusterName)
	}

	activeCluster, err := aws_operations.WaitForClusterActive(ctx, eksClient, clusterName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed while waiting for EKS cluster %s to become active", clusterName)
	}

	sgId, err := aws_operations.CreateNodeSecurityGroup(ctx, ec2Client, vpcId, clusterName, activeCluster.ResourcesVpcConfig.SecurityGroupIds)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create security groups")
	}

	clsObject, err := NewFromExisting(ctx, b.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get cluster client for cluster %s", clusterName)
	}

	err = aws_operations.AuthorizeNodeGroup(clsObject.Client(), nodeRoleArn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to authorize node group to access cluster %s", clusterName)
	}

	amiId, err := aws_operations.ResolveAMI(ctx, ec2Client, cfg.Region, minorVersion(b.clusterVersion), b.nodeMachineType, v1alpha5.DefaultNodeImageFamily)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve AMI")
	}

	clusterCfg := b.buildClusterConfig(cfg.Region, amiId, subnetAvZones)
	ng := clusterCfg.NodeGroups[0]
	clusterCfg.VPC.ID = vpcId
	ng.Subnets = subnetIDs
	ng.SecurityGroups.AttachIDs = []string{sgId}
	ng.IAM.InstanceRoleARN = nodeRoleArn

	err = clusterCfg.SetClusterState(activeCluster)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create cluster state object for cluster %s", clusterName)
	}

	err = aws_operations.CreateNodeGroup(ctx, eksClient, ec2Client, clusterCfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS node group for cluster %s", clusterName)
	}

	// init again to get a newer credential
	finalClusterObject, err := NewFromExisting(ctx, b.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get cluster client for cluster %s", clusterName)
	}
	return finalClusterObject, nil
}

func minorVersion(v *semver.Version) string {
	fullStr := v.String()
	lastIndexOfDot := strings.LastIndex(fullStr, ".")
	if lastIndexOfDot == -1 {
		lastIndexOfDot = 1
	}
	return fullStr[:lastIndexOfDot]
}

func (b *Builder) buildClusterConfig(region, amiId string, subnetAvZones []string) *eksctlapi.ClusterConfig {
	clusterCfg := eksctlapi.NewClusterConfig()

	clusterCfg.Metadata.Name = b.Name
	clusterCfg.Metadata.Region = region
	clusterCfg.Metadata.Version = minorVersion(b.clusterVersion)
	clusterCfg.KubernetesNetworkConfig.ServiceIPv4CIDR = aws_operations.DefaultKubernetesSvcCIDR
	clusterCfg.Status = &eksctlapi.ClusterStatus{}

	ng := clusterCfg.NewNodeGroup()
	ng.Name = aws_operations.DefaultNodeGroupName
	ng.ContainerRuntime = aws.String(eksctlapi.ContainerRuntimeContainerD)
	ng.AMIFamily = v1alpha5.DefaultNodeImageFamily
	ng.AMI = amiId
	ng.InstanceType = b.nodeMachineType
	ng.AvailabilityZones = subnetAvZones
	ng.ScalingConfig = &v1alpha5.ScalingConfig{
		DesiredCapacity: aws.Int(1),
		MinSize:         aws.Int(1),
		MaxSize:         aws.Int(1),
	}

	nodeKeyName := os.Getenv(envKeyNodeSSHKeyName)
	if nodeKeyName != "" {
		ng.SSH.Allow = aws.Bool(true)
		ng.SSH.PublicKeyName = aws.String(nodeKeyName)
	}

	return clusterCfg
}
