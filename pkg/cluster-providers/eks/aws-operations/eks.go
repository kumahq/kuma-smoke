package aws_operations

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/pkg/errors"
	"github.com/weaveworks/eksctl/pkg/ami"
	eksctlapi "github.com/weaveworks/eksctl/pkg/apis/eksctl.io/v1alpha5"
	"github.com/weaveworks/eksctl/pkg/authconfigmap"
	eksiam "github.com/weaveworks/eksctl/pkg/iam"
	"github.com/weaveworks/eksctl/pkg/nodebootstrap"
	"k8s.io/client-go/kubernetes"
	"time"
)

const (
	DefaultNodeGroupName     = "default-node-group"
	DefaultKubernetesSvcCIDR = "172.20.0.0/16"
	kubernetesTagFormat      = "kubernetes.io/cluster/%s"
)

func CreateCluster(ctx context.Context, eksClient *eks.Client,
	clusterName, clusterRoleArn, version, cpSgId string, subnetIDs []string) (*types.Cluster, error) {
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
		KubernetesNetworkConfig: &types.KubernetesNetworkConfigRequest{
			ServiceIpv4Cidr: aws.String(DefaultKubernetesSvcCIDR),
		},
	}

	clusterOutput, err := eksClient.CreateCluster(ctx, eksCreateInput)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create EKS cluster %s", clusterName)
	}
	return clusterOutput.Cluster, nil
}

func WaitForClusterActive(ctx context.Context, eksClient *eks.Client, clusterName string) (*types.Cluster, error) {
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

func AuthorizeNodeGroup(clientSet kubernetes.Interface, nodeRoleArn string) error {
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

func CreateNodeGroup(ctx context.Context, eksClient *eks.Client, ec2Client *ec2.Client, clusterCfg *eksctlapi.ClusterConfig) error {
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

func ResolveAMI(ctx context.Context, ec2Client *ec2.Client, region, k8sMinorVersion, instanceType, amiFamily string) (string, error) {
	resolver := ami.NewAutoResolver(ec2Client)

	id, err := resolver.Resolve(ctx, region, k8sMinorVersion, instanceType, amiFamily)
	if err != nil {
		return "", errors.Wrap(err, "unable to determine AMI to use")
	}
	return id, nil
}

func DeleteNodeGroup(ctx context.Context, eksClient *eks.Client, clusterName string) (string, string, error) {
	var notFoundErr *types.ResourceNotFoundException
	describeNGInput := &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(DefaultNodeGroupName),
	}
	ngInfo, err := eksClient.DescribeNodegroup(ctx, describeNGInput)
	if err != nil {
		if errors.As(err, &notFoundErr) {
			// the node group had already been deleted
			return "", "", nil
		} else {
			return "", "", errors.Wrapf(err, "failed to describe node group %s of cluster %s", DefaultNodeGroupName, clusterName)
		}
	}

	nodeGroupInput := &eks.DeleteNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(DefaultNodeGroupName),
	}
	_, err = eksClient.DeleteNodegroup(ctx, nodeGroupInput)
	if err != nil {
		return "", "", err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(DefaultNodeGroupName),
			}
			_, err := eksClient.DescribeNodegroup(ctx, describeInput)
			if err != nil {
				if errors.As(err, &notFoundErr) {
					// the node group has already been deleted successfully
					return aws.ToString(ngInfo.Nodegroup.NodeRole), aws.ToString(ngInfo.Nodegroup.LaunchTemplate.Id), nil
				} else {
					return "", "", errors.Wrap(err, fmt.Sprintf("failed to describe node group %s of cluster %s", DefaultNodeGroupName, clusterName))
				}
			}
		}
	}
}

func DeleteNodeLaunchTemplate(ctx context.Context, ec2Client *ec2.Client, launchTemplateId string) error {
	deleteLaunchTmplInput := &ec2.DeleteLaunchTemplateInput{
		LaunchTemplateId: aws.String(launchTemplateId),
	}
	_, err := ec2Client.DeleteLaunchTemplate(ctx, deleteLaunchTmplInput)
	if err != nil {
		return errors.Wrapf(err, "failed to delete node launch template %s", launchTemplateId)
	}
	return nil
}

func DeleteCluster(ctx context.Context, eksClient *eks.Client, clusterName string) error {
	var notFoundErr *types.ResourceNotFoundException
	clusterInput := &eks.DeleteClusterInput{
		Name: aws.String(clusterName),
	}
	_, err := eksClient.DeleteCluster(ctx, clusterInput)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeClusterInput{
				Name: aws.String(clusterName),
			}
			_, err := eksClient.DescribeCluster(ctx, describeInput)
			if err != nil {
				if errors.As(err, &notFoundErr) {
					// the cluster has already been deleted successfully
					return nil
				} else {
					return errors.Wrap(err, fmt.Sprintf("failed to describe EKS cluster %s to check delete progress", clusterName))
				}
			}
		}
	}
}
