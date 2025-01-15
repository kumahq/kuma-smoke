package eks

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2Types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	err_pkg "github.com/pkg/errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kong/kubernetes-testing-framework/pkg/clusters"
)

// -----------------------------------------------------------------------------
// EKS Cluster
// -----------------------------------------------------------------------------

// Cluster is a clusters.Cluster implementation backed by AWS Elastic Kubernetes Service (EKS)
type Cluster struct {
	name            string
	waitForTeardown bool
	client          *kubernetes.Clientset
	cfg             *rest.Config
	addons          clusters.Addons
	l               *sync.RWMutex
	ipFamily        clusters.IPFamily
}

// NewFromExisting provides a new clusters.Cluster backed by an existing EKS cluster,
// but allows some of the configuration to be filled in from the ENV instead of arguments.
func NewFromExisting(ctx context.Context, name string) (*Cluster, error) {
	err := guardOnEnv()
	if err != nil {
		return nil, err
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err_pkg.Wrap(err, "failed to load AWS SDK config")
	}

	restCfg, kubeCfg, err := clientForCluster(ctx, cfg, name)
	if err != nil {
		return nil, err_pkg.Wrapf(err, "failed to get kube client for cluster %s", name)
	}
	return &Cluster{
		name:   name,
		client: kubeCfg,
		cfg:    restCfg,
		addons: make(clusters.Addons),
		l:      &sync.RWMutex{},
	}, nil
}

func guardOnEnv() error {
	if os.Getenv(envAccessKeyId) == "" {
		return errors.New(envAccessKeyId + " is not set")
	}
	if os.Getenv(envAccessKey) == "" {
		return errors.New(envAccessKey + " is not set")
	}
	if os.Getenv(envRegion) == "" {
		return errors.New(envRegion + " is not set")
	}
	return nil
}

// -----------------------------------------------------------------------------
// EKS Cluster - Cluster Implementation
// -----------------------------------------------------------------------------

func (c *Cluster) Name() string {
	return c.name
}

func (c *Cluster) Type() clusters.Type {
	return eksClusterType
}

func (c *Cluster) Version() (semver.Version, error) {
	versionInfo, err := c.Client().ServerVersion()
	if err != nil {
		return semver.Version{}, err
	}
	return semver.Parse(strings.TrimPrefix(versionInfo.String(), "v"))
}

func (c *Cluster) Cleanup(ctx context.Context) error {
	c.l.Lock()
	defer c.l.Unlock()

	err := guardOnEnv()
	if err != nil {
		return err
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err_pkg.Wrap(err, "failed to load AWS SDK config")
	}

	eksClient := eks.NewFromConfig(cfg)
	ec2Client := ec2.NewFromConfig(cfg)
	iamClient := iam.NewFromConfig(cfg)

	activeCluster, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(c.name),
	})
	if err != nil {
		return err_pkg.Wrap(err, "failed to read cluster information")
	}

	vpcID := activeCluster.Cluster.ResourcesVpcConfig.VpcId
	ngRole, launchTemplateId, err := deleteNodeGroup(ctx, eksClient, c.name)
	if err != nil {
		return err
	}
	if launchTemplateId != "" {
		err = deleteNodeLaunchTemplate(ctx, ec2Client, launchTemplateId)
		if err != nil {
			return err
		}
	}

	err = deleteRoles(ctx, iamClient, []string{ngRole, *activeCluster.Cluster.RoleArn})
	if err != nil {
		return err
	}

	err = deleteCluster(ctx, eksClient, c.name)
	if err != nil {
		return err
	}

	err = deleteVPC(ctx, ec2Client, *vpcID)
	if err != nil {
		return err
	}

	return nil
}

func (c *Cluster) Client() *kubernetes.Clientset {
	return c.client
}

func (c *Cluster) Config() *rest.Config {
	return c.cfg
}

func (c *Cluster) GetAddon(name clusters.AddonName) (clusters.Addon, error) {
	c.l.RLock()
	defer c.l.RUnlock()

	for addonName, addon := range c.addons {
		if addonName == name {
			return addon, nil
		}
	}

	return nil, fmt.Errorf("addon %s not found", name)
}

func (c *Cluster) ListAddons() []clusters.Addon {
	c.l.RLock()
	defer c.l.RUnlock()

	addonList := make([]clusters.Addon, 0, len(c.addons))
	for _, v := range c.addons {
		addonList = append(addonList, v)
	}

	return addonList
}

func (c *Cluster) DeployAddon(ctx context.Context, addon clusters.Addon) error {
	c.l.Lock()
	if _, ok := c.addons[addon.Name()]; ok {
		c.l.Unlock()
		return fmt.Errorf("addon component %s is already loaded into cluster %s", addon.Name(), c.Name())
	}
	c.addons[addon.Name()] = addon
	c.l.Unlock()

	return addon.Deploy(ctx, c)
}

func (c *Cluster) DeleteAddon(ctx context.Context, addon clusters.Addon) error {
	c.l.Lock()
	defer c.l.Unlock()

	if _, ok := c.addons[addon.Name()]; !ok {
		return nil
	}

	if err := addon.Delete(ctx, c); err != nil {
		return err
	}

	delete(c.addons, addon.Name())

	return nil
}

// DumpDiagnostics produces diagnostics data for the cluster at a given time.
// It uses the provided meta string to write to meta.txt file which will allow
// for diagnostics identification.
// It returns the path to directory containing all the diagnostic files and an error.
func (c *Cluster) DumpDiagnostics(ctx context.Context, meta string) (string, error) {
	// Obtain a kubeconfig
	kubeconfig, err := clusters.TempKubeconfig(c)
	if err != nil {
		return "", err
	}
	defer os.Remove(kubeconfig.Name())

	// create a tempdir
	outDir, err := os.MkdirTemp(os.TempDir(), clusters.DiagnosticOutDirectoryPrefix)
	if err != nil {
		return "", err
	}

	// for each Pod, run kubectl logs
	pods, err := c.Client().CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return outDir, err
	}
	logsDir := filepath.Join(outDir, "pod_logs")
	err = os.Mkdir(logsDir, 0o750) //nolint:mnd
	if err != nil {
		return outDir, err
	}
	failedPods := make(map[string]error)
	for _, pod := range pods.Items {
		podLogOut, err := os.Create(filepath.Join(logsDir, fmt.Sprintf("%s_%s", pod.Namespace, pod.Name)))
		if err != nil {
			failedPods[fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)] = err
			continue
		}
		cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig.Name(), "logs", "--all-containers", "-n", pod.Namespace, pod.Name) //nolint:gosec
		cmd.Stdout = podLogOut
		if err := cmd.Run(); err != nil {
			failedPods[fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)] = err
			continue
		}
		defer podLogOut.Close()
	}
	if len(failedPods) > 0 {
		failedPodOut, err := os.Create(filepath.Join(outDir, "pod_logs_failures.txt"))
		if err != nil {
			return outDir, err
		}
		defer failedPodOut.Close()
		for failed, reason := range failedPods {
			_, err = failedPodOut.WriteString(fmt.Sprintf("%s: %v\n", failed, reason))
			if err != nil {
				return outDir, err
			}
		}
	}

	err = clusters.DumpDiagnostics(ctx, c, meta, outDir)

	return outDir, err
}

func (c *Cluster) IPFamily() clusters.IPFamily {
	return c.ipFamily
}

func deleteNodeGroup(ctx context.Context, eksClient *eks.Client, clusterName string) (string, string, error) {
	var notFoundErr *types.ResourceNotFoundException
	describeNGInput := &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(defaultNodeGroupName),
	}
	ngInfo, err := eksClient.DescribeNodegroup(ctx, describeNGInput)
	if err != nil {
		if errors.As(err, &notFoundErr) {
			// the node group had already been deleted
			return "", "", nil
		} else {
			return "", "", err_pkg.Wrapf(err, "failed to describe node group %s of cluster %s", defaultNodeGroupName, clusterName)
		}
	}

	nodeGroupInput := &eks.DeleteNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(defaultNodeGroupName),
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
				NodegroupName: aws.String(defaultNodeGroupName),
			}
			_, err := eksClient.DescribeNodegroup(ctx, describeInput)
			if err != nil {
				if errors.As(err, &notFoundErr) {
					// the node group has already been deleted successfully
					return aws.ToString(ngInfo.Nodegroup.NodeRole), aws.ToString(ngInfo.Nodegroup.LaunchTemplate.Id), nil
				} else {
					return "", "", err_pkg.Wrap(err, fmt.Sprintf("failed to describe node group %s of cluster %s", defaultNodeGroupName, clusterName))
				}
			}
		}
	}
}

func deleteNodeLaunchTemplate(ctx context.Context, ec2Client *ec2.Client, launchTemplateId string) error {
	deleteLaunchTmplInput := &ec2.DeleteLaunchTemplateInput{
		LaunchTemplateId: aws.String(launchTemplateId),
	}
	_, err := ec2Client.DeleteLaunchTemplate(ctx, deleteLaunchTmplInput)
	if err != nil {
		return err_pkg.Wrapf(err, "failed to delete node launch template %s", launchTemplateId)
	}
	return nil
}

func deleteCluster(ctx context.Context, eksClient *eks.Client, clusterName string) error {
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
					return err_pkg.Wrap(err, fmt.Sprintf("failed to describe EKS cluster %s", clusterName))
				}
			}
		}
	}
}

func deleteVPC(ctx context.Context, ec2Client *ec2.Client, vpcID string) error {
	routeTablesOutput, err := ec2Client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2Types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to list route tables in VPC %s", vpcID)
	}

	for _, rt := range routeTablesOutput.RouteTables {
		isMain := false
		for _, assoc := range rt.Associations {
			if assoc.Main != nil && *assoc.Main {
				isMain = true
				break
			}
		}
		if isMain {
			continue
		}

		for _, assoc := range rt.Associations {
			if assoc.RouteTableAssociationId != nil {
				_, err := ec2Client.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{
					AssociationId: assoc.RouteTableAssociationId,
				})
				if err != nil {
					return err_pkg.Wrapf(err, "failed to disassociate route table association %s for route table %s", *assoc.RouteTableAssociationId, *rt.RouteTableId)
				}
			}
		}

		_, err := ec2Client.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
			RouteTableId: rt.RouteTableId,
		})
		if err != nil {
			return err_pkg.Wrapf(err, "failed to delete route table %s", *rt.RouteTableId)
		}
	}

	subnetsOutput, err := ec2Client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []ec2Types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to describe subnets in VPC %s", vpcID)
	}

	for _, subnet := range subnetsOutput.Subnets {
		_, err := ec2Client.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
			SubnetId: subnet.SubnetId,
		})
		if err != nil {
			return err_pkg.Wrapf(err, "failed to delete subnet %s", *subnet.SubnetId)
		}
	}

	igwsOutput, err := ec2Client.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2Types.Filter{
			{Name: aws.String("attachment.vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to describe internet gateways in VPC %s", vpcID)
	}

	for _, igw := range igwsOutput.InternetGateways {
		_, err := ec2Client.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
			VpcId:             aws.String(vpcID),
		})
		if err != nil {
			return err_pkg.Wrapf(err, "failed to detach internet gateway %s", *igw.InternetGatewayId)
		}

		_, err = ec2Client.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
		})
		if err != nil {
			return err_pkg.Wrapf(err, "failed to delete internet gateway %s", *igw.InternetGatewayId)
		}
	}

	sgOutput, err := ec2Client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2Types.Filter{
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to describe security groups in VPC %s", vpcID)
	}

	for _, sg := range sgOutput.SecurityGroups {
		if sg.GroupName != nil && *sg.GroupName == "default" {
			continue
		}

		for _, ingress := range sg.IpPermissions {
			_, err := ec2Client.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
				GroupId:       sg.GroupId,
				IpPermissions: []ec2Types.IpPermission{ingress},
			})
			if err != nil {
				return err_pkg.Wrapf(err, "failed to revoke a %s ingress rule on security group %s",
					aws.ToString(ingress.IpProtocol), aws.ToString(sg.GroupId))
			}
		}

		for _, egress := range sg.IpPermissionsEgress {
			_, err := ec2Client.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       sg.GroupId,
				IpPermissions: []ec2Types.IpPermission{egress},
			})
			if err != nil {
				return err_pkg.Wrapf(err, "failed to revoke a %s egress rule on security group %s",
					aws.ToString(egress.IpProtocol), aws.ToString(sg.GroupId))
			}
		}
	}

	for _, sg := range sgOutput.SecurityGroups {
		if sg.GroupName != nil && *sg.GroupName == "default" {
			continue
		}

		_, err := ec2Client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: sg.GroupId,
		})
		if err != nil {
			return err_pkg.Wrapf(err, "failed to delete security group %s", *sg.GroupId)
		}
	}

	_, err = ec2Client.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(vpcID),
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to delete VPC %s", vpcID)
	}

	return nil
}
