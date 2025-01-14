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
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
	"github.com/pkg/errors"
	"github.com/weaveworks/eksctl/pkg/ami"
	"github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	eksctlapi "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	eksiam "github.com/weaveworks/eksctl/pkg/iam"
	"github.com/weaveworks/eksctl/pkg/nodebootstrap"
	"k8s.io/client-go/kubernetes"
	"os"
	"strings"
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
	defaultNodeMachineType   = "c5.4xlarge"
	defaultNodeGroupName     = "default-node-group"
	defaultKubernetesVersion = "1.31.1"
	defaultKubernetesSvcCIDR = "172.20.0.0/16"
	defaultVPCCIDR           = "10.163.0.0/16"
	envKeyNodeSSHKeyName     = "EKS_NODE_SSH_KEY"
	kubernetesTagFormat      = "kubernetes.io/cluster/%s"
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
	clusterRoleArn, nodeRoleArn, err := createRoles(ctx, iamClient, b.Name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create IAM roles")
	}
	subnetAvZones, err := getAvailabilityZones(ctx, ec2Client, cfg.Region)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get availability zones in region %s", cfg.Region)
	}

	vpcId, subnetIDs, err := createVPC(ctx, ec2Client, subnetAvZones)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create VPC")
	}
	clusterName := b.Name

	cpSgId, err := createControlPlaneSecurityGroup(ctx, ec2Client, vpcId, clusterName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create control plane security group in VPC %s", vpcId)
	}

	eksCreateInput := &eks.CreateClusterInput{
		Name:    &clusterName,
		RoleArn: &clusterRoleArn,
		Version: aws.String(version),

		ResourcesVpcConfig: &types.VpcConfigRequest{
			EndpointPrivateAccess: aws.Bool(true),
			EndpointPublicAccess:  aws.Bool(true),
			SubnetIds:             subnetIDs,
			SecurityGroupIds:      []string{cpSgId},
		},
	}

	_, err = eksClient.CreateCluster(ctx, eksCreateInput)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS cluster %s", clusterName)
	}

	activeCluster, err := waitForClusterActive(ctx, eksClient, clusterName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed while waiting for EKS cluster %s to become active", clusterName)
	}

	sgId, err := createNodeSecurityGroup(ctx, ec2Client, vpcId, clusterName, activeCluster.ResourcesVpcConfig.SecurityGroupIds)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create security groups")
	}

	clsObject, err := NewFromExisting(ctx, b.Name)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get cluster client for cluster %s", clusterName)
	}

	err = authorizeNodeGroup(clsObject.Client(), nodeRoleArn)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to authorize node group to access cluster %s", clusterName)
	}

	amiId, err := resolveAMI(ctx, ec2Client, cfg.Region, minorVersion(b.clusterVersion), b.nodeMachineType, v1alpha5.DefaultNodeImageFamily)
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

	err = createNodeGroup(ctx, eksClient, ec2Client, clusterCfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS node group for cluster %s", clusterName)
	}

	return clsObject, nil
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
	clusterCfg.KubernetesNetworkConfig.ServiceIPv4CIDR = defaultKubernetesSvcCIDR
	clusterCfg.Status = &eksctlapi.ClusterStatus{}

	ng := clusterCfg.NewNodeGroup()
	ng.Name = defaultNodeGroupName
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

func getAvailabilityZones(ctx context.Context, ec2Client *ec2.Client, region string) ([]string, error) {
	availabilityZonesOutput, err := ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to describe availability zones")
	}
	var subnetAvZones []string
	for _, az := range availabilityZonesOutput.AvailabilityZones {
		if az.State == ec2Types.AvailabilityZoneStateAvailable && len(subnetAvZones) < 2 {
			subnetAvZones = append(subnetAvZones, *az.ZoneName)
		}
	}
	if len(subnetAvZones) < 2 {
		return nil, errors.Wrapf(err, "there is no sufficient availability zones available in region %s", region)
	}
	return subnetAvZones, nil
}

func createVPC(ctx context.Context, ec2Client *ec2.Client, subnetAvZones []string) (string, []string, error) {
	vpcOutput, err := ec2Client.CreateVpc(ctx, &ec2.CreateVpcInput{
		CidrBlock: aws.String(defaultVPCCIDR),
	})
	if err != nil {
		return "", nil, errors.Wrap(err, "failed to create VPC")
	}

	vpcID := *vpcOutput.Vpc.VpcId
	_, err = ec2Client.ModifyVpcAttribute(context.TODO(), &ec2.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcID),
		EnableDnsSupport: &ec2Types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to enable DNS support for VPC %s", vpcID)
	}
	_, err = ec2Client.ModifyVpcAttribute(context.TODO(), &ec2.ModifyVpcAttributeInput{
		VpcId: aws.String(vpcID),
		EnableDnsHostnames: &ec2Types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to enable DNS support for VPC %s", vpcID)
	}

	igwOutput, err := ec2Client.CreateInternetGateway(ctx, &ec2.CreateInternetGatewayInput{})
	if err != nil {
		return "", nil, errors.Wrap(err, "unable to create Internet Gateway")
	}
	_, err = ec2Client.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
		InternetGatewayId: igwOutput.InternetGateway.InternetGatewayId,
		VpcId:             vpcOutput.Vpc.VpcId,
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "unable to add Internet Gateway %s within the VPC %s", *igwOutput.InternetGateway.InternetGatewayId, vpcID)
	}
	rtOutput, err := ec2Client.CreateRouteTable(ctx, &ec2.CreateRouteTableInput{
		VpcId: vpcOutput.Vpc.VpcId,
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create Route Table")
	}
	_, err = ec2Client.CreateRoute(ctx, &ec2.CreateRouteInput{
		RouteTableId:         rtOutput.RouteTable.RouteTableId,
		GatewayId:            igwOutput.InternetGateway.InternetGatewayId,
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
	})
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create default egress route for Route Table %s",
			*rtOutput.RouteTable.RouteTableId)
	}

	subnetId1, err := createSubnet(ctx, ec2Client, vpcID, "10.0.1.0/24", subnetAvZones[0], *rtOutput.RouteTable.RouteTableId)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create subnet within the VPC %s", vpcID)
	}
	subnetId2, err := createSubnet(ctx, ec2Client, vpcID, "10.0.2.0/24", subnetAvZones[1], *rtOutput.RouteTable.RouteTableId)
	if err != nil {
		return "", nil, errors.Wrapf(err, "failed to create subnet within the VPC %s", vpcID)
	}

	subnetIDs := []string{subnetId1, subnetId2}
	return vpcID, subnetIDs, nil
}

func createSubnet(ctx context.Context, ec2Client *ec2.Client, vpcID, cidrBlock, availabilityZone, routeTableId string) (string, error) {
	subnet1Output, err := ec2Client.CreateSubnet(ctx, &ec2.CreateSubnetInput{
		VpcId:            aws.String(vpcID),
		CidrBlock:        aws.String(cidrBlock),
		AvailabilityZone: aws.String(availabilityZone),
	})
	if err != nil {
		return "", errors.Wrapf(err, "failed to create subnet within the VPC %s", vpcID)
	}

	subnetId := subnet1Output.Subnet.SubnetId
	_, err = ec2Client.ModifySubnetAttribute(ctx, &ec2.ModifySubnetAttributeInput{
		SubnetId:            subnetId,
		MapPublicIpOnLaunch: &ec2Types.AttributeBooleanValue{Value: aws.Bool(true)},
	})
	if err != nil {
		return "", errors.Wrapf(err, "unable to modify subnet %s to enable public IP assignment", *subnetId)
	}

	if routeTableId != "" {
		_, err = ec2Client.AssociateRouteTable(ctx, &ec2.AssociateRouteTableInput{
			RouteTableId: aws.String(routeTableId),
			SubnetId:     subnetId,
		})
		if err != nil {
			return "", errors.Wrapf(err, "failed to associate Route Table %s with subnet %s", routeTableId, *subnetId)
		}
	}
	return *subnetId, nil
}

func createControlPlaneSecurityGroup(ctx context.Context, ec2Client *ec2.Client, vpcId, namePrefix string) (string, error) {
	sg1Output, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-cp", namePrefix)),
		Description: aws.String("Allow communication between the control plane and worker nodes"),
		VpcId:       aws.String(vpcId),
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to create security group")
	}
	return *sg1Output.GroupId, nil
}

func createNodeSecurityGroup(ctx context.Context, ec2Client *ec2.Client, vpcId, namePrefix string, cpDefaultSecurityGroupIds []string) (string, error) {
	sgOutput, err := ec2Client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(fmt.Sprintf("%s-shared-by-all-nodes", namePrefix)),
		Description: aws.String("All communication between all nodes in the cluster"),
		VpcId:       aws.String(vpcId),
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to create node security group")
	}

	for _, sgId := range cpDefaultSecurityGroupIds {
		_, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: sgOutput.GroupId,
			IpPermissions: []ec2Types.IpPermission{
				{
					IpProtocol: aws.String("-1"),
					UserIdGroupPairs: []ec2Types.UserIdGroupPair{
						{
							GroupId: aws.String(sgId),
						},
					},
				},
			},
		})
		if err != nil {
			return "", errors.Wrapf(err, "failed to authorize inbound traffic from control plane security group %s to node security group %s",
				sgId, *sgOutput.GroupId)
		}

		_, err = ec2Client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: aws.String(sgId),
			IpPermissions: []ec2Types.IpPermission{
				{
					IpProtocol: aws.String("-1"),
					UserIdGroupPairs: []ec2Types.UserIdGroupPair{
						{
							GroupId: sgOutput.GroupId,
						},
					},
				},
			},
		})
		if err != nil {
			return "", errors.Wrapf(err, "failed to authorize inbound traffic from node security group %s to control plane security group %s",
				*sgOutput.GroupId, sgId)
		}
	}

	return *sgOutput.GroupId, nil
}

func authorizeNodeGroup(clientSet kubernetes.Interface, nodeRoleArn string) error {
	acm, err := authconfigmap.NewFromClientSet(clientSet)
	if err != nil {
		return err
	}

	nodeGroupRoles := authconfigmap.RoleNodeGroupGroups

	identity, err := eksiam.NewIdentity(nodeRoleArn, authconfigmap.RoleNodeGroupUsername, nodeGroupRoles)
	if err != nil {
		return err
	}

	if err := acm.AddIdentity(identity); err != nil {
		return errors.Wrap(err, "adding nodegroup to auth ConfigMap")
	}
	if err := acm.Save(); err != nil {
		return errors.Wrap(err, "saving auth ConfigMap")
	}
	return nil
}

func waitForClusterActive(ctx context.Context, eksClient *eks.Client, clusterName string) (*types.Cluster, error) {
	childCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-childCtx.Done():
			return nil, childCtx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeClusterInput{
				Name: &clusterName,
			}
			resp, err := eksClient.DescribeCluster(ctx, describeInput)
			if err != nil {
				return nil, errors.Wrap(err, fmt.Sprintf("failed to describe EKS cluster %s", clusterName))
			}

			status := resp.Cluster.Status
			if status == types.ClusterStatusActive {
				return resp.Cluster, nil
			}
		}
	}
}

func createNodeGroup(ctx context.Context, eksClient *eks.Client, ec2Client *ec2.Client, clusterCfg *eksctlapi.ClusterConfig) error {
	nodeGroup := clusterCfg.NodeGroups[0]
	launchTemplateId, err := createNodeLaunchTemplate(ctx, ec2Client, clusterCfg)
	if err != nil {
		return errors.Wrap(err, "failed to create launch template")
	}

	input := &eks.CreateNodegroupInput{
		ClusterName:   aws.String(clusterCfg.Metadata.Name),
		NodegroupName: aws.String(nodeGroup.Name),
		NodeRole:      aws.String(nodeGroup.IAM.InstanceRoleARN),
		Subnets:       nodeGroup.Subnets,
		ScalingConfig: &types.NodegroupScalingConfig{
			MinSize:     aws.Int32(int32(aws.ToInt(nodeGroup.MinSize))),
			MaxSize:     aws.Int32(int32(aws.ToInt(nodeGroup.MaxSize))),
			DesiredSize: aws.Int32(int32(aws.ToInt(nodeGroup.DesiredCapacity))),
		},
		LaunchTemplate: &types.LaunchTemplateSpecification{
			Id: aws.String(launchTemplateId),
		},
	}

	_, err = eksClient.CreateNodegroup(ctx, input)
	if err != nil {
		return err
	}

	return waitForNodeGroupReady(ctx, eksClient, clusterCfg.Metadata.Name, nodeGroup.Name)
}

func createNodeLaunchTemplate(ctx context.Context, ec2Client *ec2.Client, clusterCfg *eksctlapi.ClusterConfig) (string, error) {
	nodeGroup := clusterCfg.NodeGroups[0]
	bootstrap := nodebootstrap.NewAL2Bootstrapper(clusterCfg, nodeGroup, nodeGroup.ClusterDNS)
	userdata, err := bootstrap.UserData()
	if err != nil {
		return "", errors.Wrap(err, "failed to generate instance bootstrap user data")
	}

	input := &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(fmt.Sprintf("%s-node-template", clusterCfg.Metadata.Name)),
		LaunchTemplateData: &ec2Types.RequestLaunchTemplateData{
			ImageId:          aws.String(nodeGroup.AMI),
			InstanceType:     ec2Types.InstanceType(nodeGroup.InstanceType),
			SecurityGroupIds: nodeGroup.SecurityGroups.AttachIDs,
			BlockDeviceMappings: []ec2Types.LaunchTemplateBlockDeviceMappingRequest{
				{
					DeviceName: aws.String("/dev/xvda"),
					Ebs: &ec2Types.LaunchTemplateEbsBlockDeviceRequest{
						VolumeSize: aws.Int32(int32(aws.ToInt(nodeGroup.VolumeSize))),
						VolumeType: ec2Types.VolumeType(aws.ToString(nodeGroup.VolumeType)),
					},
				},
			},
			UserData: aws.String(userdata),
			TagSpecifications: []ec2Types.LaunchTemplateTagSpecificationRequest{
				{
					ResourceType: ec2Types.ResourceTypeInstance,
					Tags: []ec2Types.Tag{
						{
							Key:   aws.String(fmt.Sprintf(kubernetesTagFormat, clusterCfg.Metadata.Name)),
							Value: aws.String("owned"),
						},
					},
				},
			},
		},
	}

	if nodeGroup.SSH.PublicKeyName != nil {
		input.LaunchTemplateData.KeyName = nodeGroup.SSH.PublicKeyName
	}

	output, err := ec2Client.CreateLaunchTemplate(ctx, input)
	if err != nil {
		return "", errors.Wrap(err, "failed to create launch template")
	}

	return *output.LaunchTemplate.LaunchTemplateId, nil
}

func waitForNodeGroupReady(ctx context.Context, eksClient *eks.Client, clusterName, nodeGroupName string) error {
	childCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-childCtx.Done():
			return childCtx.Err()
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

func resolveAMI(ctx context.Context, ec2Client *ec2.Client, region, k8sMajorVersion, instanceType, amiFamily string) (string, error) {
	resolver := ami.NewAutoResolver(ec2Client)

	id, err := resolver.Resolve(ctx, region, k8sMajorVersion, instanceType, amiFamily)
	if err != nil {
		return "", errors.Wrap(err, "unable to determine AMI to use")
	}
	return id, nil
}
