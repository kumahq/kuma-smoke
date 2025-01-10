package eks

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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

	eksOutput, err := eksClient.DescribeCluster(context.TODO(), &eks.DescribeClusterInput{
		Name: aws.String(c.name),
	})
	if err != nil {
		return err_pkg.Wrap(err, "failed to read cluster information")
	}

	subnetIDs := eksOutput.Cluster.ResourcesVpcConfig.SubnetIds
	ec2Output, err := ec2Client.DescribeSubnets(context.TODO(), &ec2.DescribeSubnetsInput{
		SubnetIds: subnetIDs,
	})
	if err != nil {
		return err_pkg.Wrap(err, "failed to read information of subnets on cluster")
	}

	vpcID := *ec2Output.Subnets[0].VpcId
	ngRole, err := deleteNodeGroup(ctx, eksClient, c.name)
	if err != nil {
		return err
	}

	err = deleteCluster(ctx, eksClient, c.name)
	if err != nil {
		return err
	}

	err = deleteVPC(ctx, ec2Client, vpcID)
	if err != nil {
		return err
	}

	iamClient := iam.NewFromConfig(cfg)
	_, err = iamClient.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(ngRole),
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to delete node role %s", ngRole)
	}
	_, err = iamClient.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: eksOutput.Cluster.RoleArn,
	})
	if err != nil {
		return err_pkg.Wrapf(err, "failed to delete cluster role %s", ngRole)
	}

	return nil
}

func deleteNodeGroup(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
	describeNGInput := &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(defaultNodeGroupName),
	}
	ngInfo, err := eksClient.DescribeNodegroup(ctx, describeNGInput)
	if err != nil {
		// todo: detect if the nodegroup is already deleted
		return "", err_pkg.Wrapf(err, "failed to describe node group %s", defaultNodeGroupName)
	}

	nodeGroupInput := &eks.DeleteNodegroupInput{
		ClusterName:   aws.String(clusterName),
		NodegroupName: aws.String(defaultNodeGroupName),
	}
	_, err = eksClient.DeleteNodegroup(ctx, nodeGroupInput)
	if err != nil {
		return "", err
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
			describeInput := &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(defaultNodeGroupName),
			}
			_, err := eksClient.DescribeNodegroup(ctx, describeInput)
			if err != nil {
				// todo: detect if the nodegroup is already deleted
				return aws.ToString(ngInfo.Nodegroup.NodeRole),
					err_pkg.Wrapf(err, "failed to describe node group %s", defaultNodeGroupName)
			}
		}
	}

}

func deleteCluster(ctx context.Context, eksClient *eks.Client, clusterName string) error {
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
			resp, err := eksClient.DescribeCluster(ctx, describeInput)
			if err != nil {
				// todo: detect if cluster is already deleted
				return err_pkg.Wrap(err, fmt.Sprintf("failed to describe EKS cluster %s", clusterName))
			}

			status := resp.Cluster.Status
			if status == types.ClusterStatusFailed {
				return nil
			}
		}
	}
}

func deleteVPC(ctx context.Context, ec2Client *ec2.Client, vpcId string) error {
	deleteVPCInput := &ec2.DeleteVpcInput{
		VpcId: aws.String(vpcId),
	}
	_, err := ec2Client.DeleteVpc(ctx, deleteVPCInput)
	return err
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
