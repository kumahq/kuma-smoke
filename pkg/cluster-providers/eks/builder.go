package eks

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/pkg/errors"
	"time"
)

// Builder generates clusters.Cluster objects backed by GKE given
// provided configuration options.
type Builder struct {
	Name            string
	waitForTeardown bool

	addons          clusters.Addons
	clusterVersion  *semver.Version
	nodeMachineType string
}

const (
	defaultNodeMachineType = "c5.2xlarge"
	defaultNodeGroupName   = "default-node-group"
)

// NewBuilder provides a new *Builder object.
func NewBuilder() *Builder {
	return &Builder{
		Name:            fmt.Sprintf("t-%s", uuid.NewString()),
		nodeMachineType: defaultNodeMachineType,
		addons:          make(clusters.Addons),
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

	stsClient := sts.NewFromConfig(cfg)
	ec2Client := ec2.NewFromConfig(cfg)
	eksClient := eks.NewFromConfig(cfg)

	callerIdentity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get caller identity")
	}

	accountId := *callerIdentity.Account
	_, subnetIDs, err := createVPC(ctx, ec2Client, cfg.Region)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create VPC")
	}

	clusterName := b.Name
	clusterRoleArn := fmt.Sprintf("arn:aws:iam::%s:role/EKSClusterRole", accountId)

	eksCreateInput := &eks.CreateClusterInput{
		Name:    &clusterName,
		RoleArn: &clusterRoleArn,
		ResourcesVpcConfig: &types.VpcConfigRequest{
			SubnetIds: subnetIDs,
		},
	}

	_, err = eksClient.CreateCluster(ctx, eksCreateInput)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS cluster %s", clusterName)
	}

	err = waitForClusterActive(ctx, eksClient, clusterName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed while waiting for EKS cluster %s to become active", clusterName)
	}

	nodeRoleArn := fmt.Sprintf("arn:aws:iam::%s:role/EKSNodeRole", accountId)
	err = createNodeGroup(ctx, eksClient, clusterName, nodeRoleArn, b.nodeMachineType, subnetIDs)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS node group for cluster %s", clusterName)
	}

	return NewFromExisting(ctx, b.Name)
}

func createVPC(ctx context.Context, client *ec2.Client, region string) (string, []string, error) {
	vpcOutput, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String("10.0.0.0/16"),
	})
	if err != nil {
		return "", nil, errors.Wrap(err, "failed to create VPC")
	}

	vpcID := *vpcOutput.Vpc.VpcId
	_, err = client.ModifyVpcAttribute(context.TODO(), &ec2.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcID),
		EnableDnsSupport: &ec2Types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to enable DNS support for VPC %s", vpcID)
	}
	_, err = client.ModifyVpcAttribute(context.TODO(), &ec2.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcID),
		EnableDnsHostnames: &ec2Types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to enable DNS support for VPC %s", vpcID)
	}

	availabilityZonesOutput, err := client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return "", nil, errors.Wrap(err, "failed to describe availability zones")
	}
	var subnetAvZones []string
	for _, az := range availabilityZonesOutput.AvailabilityZones {
		if az.State == ec2Types.AvailabilityZoneStateAvailable && len(subnetAvZones) < 2 {
			subnetAvZones = append(subnetAvZones, *az.ZoneName)
		}
	}
	if len(subnetAvZones) < 2 {
		return "", nil, errors.Wrapf(err, "there is no sufficient availability zones available in region %s", region)
	}

	subnet1Output, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.0.1.0/24"),
		AvailabilityZone: aws.String(subnetAvZones[0]),
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create subnet within the VPC %s", vpcID)
	}

	subnet2Output, err := client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String("10.0.2.0/24"),
		AvailabilityZone: aws.String(subnetAvZones[1]),
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create subnet within the VPC %s", vpcID)
	}

	subnetIDs := []string{
		*subnet1Output.Subnet.SubnetId,
		*subnet2Output.Subnet.SubnetId,
	}
	return vpcID, subnetIDs, nil
}

func waitForClusterActive(ctx context.Context, eksClient *eks.Client, clusterName string) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeClusterInput{
				Name: &clusterName,
			}
			resp, err := eksClient.DescribeCluster(ctx, describeInput)
			if err != nil {
				return errors.Wrap(err, fmt.Sprintf("failed to describe EKS cluster %s", clusterName))
			}

			status := resp.Cluster.Status
			if status == types.ClusterStatusActive {
				return nil
			}
		}
	}
}

func createNodeGroup(ctx context.Context, client *eks.Client, clusterName, nodeRoleArn, machineType string, subnetIDs []string) error {
	nodeGroupName := defaultNodeGroupName
	input := &eks.CreateNodegroupInput{
		ClusterName:   &clusterName,
		NodegroupName: &nodeGroupName,
		NodeRole:      &nodeRoleArn,
		Subnets:       subnetIDs,
		ScalingConfig: &types.NodegroupScalingConfig{
			MinSize:     aws.Int32(1),
			MaxSize:     aws.Int32(1),
			DesiredSize: aws.Int32(1),
		},
		AmiType:       types.AMITypesAl2X8664,
		InstanceTypes: []string{machineType},
	}

	_, err := client.CreateNodegroup(ctx, input)
	if err != nil {
		return err
	}

	return waitForNodeGroupReady(ctx, client, clusterName, nodeGroupName)
}

func waitForNodeGroupReady(ctx context.Context, eksClient *eks.Client, clusterName, nodeGroupName string) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeNodegroupInput{
				ClusterName:   &clusterName,
				NodegroupName: &nodeGroupName,
			}
			resp, err := eksClient.DescribeNodegroup(ctx, describeInput)
			if err != nil {
				return errors.Wrapf(err, "failed to describe node group %s", nodeGroupName)
			}

			status := resp.Nodegroup.Status
			if status == types.NodegroupStatusActive {
				return nil
			}
		}
	}
}
