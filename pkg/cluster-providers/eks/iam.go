package eks

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/pkg/errors"
)

func createRoles(ctx context.Context, iamClient *iam.Client, namePrefix string) (string, string, error) {
	clusterRoleArn, err := createRole(ctx, iamClient,
		namePrefix+"-EksClusterRole", "Allows access to other AWS service resources that are required to operate clusters managed by EKS.",
		[]string{"arn:aws:iam::aws:policy/AmazonEKSClusterPolicy",
			"arn:aws:iam::aws:policy/AmazonEKSVPCResourceController"},
		map[string]string{
			"CloudWatchMetricsPolicy": inlinePolicyCloudWatchMetrics,
			"ELBPermissionsPolicy":    inlinePoliciesELBPermissions,
		}, trustedEntitiesEKS,
	)
	if err != nil {
		return "", "", errors.Wrap(err, "error creating the IAM role for the cluster to use")
	}

	nodeRoleArn, err := createRole(ctx, iamClient,
		namePrefix+"-NodeInstanceRole", "Allows EC2 instances to call AWS services on your behalf.",
		[]string{"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
			"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
			"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
			"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
		}, nil, trustedEntitiesEC2,
	)
	if err != nil {
		return "", "", errors.Wrap(err, "error creating the IAM role for the nodegroup to use")
	}
	return clusterRoleArn, nodeRoleArn, nil
}

func createRole(ctx context.Context, iamClient *iam.Client,
	newRoleName string, newRoleDescription string, managedPolicyNames []string, inlinePolicies map[string]string, trustPolicy string) (string, error) {
	input := &iam.CreateRoleInput{
		RoleName:                 aws.String(newRoleName),
		Description:              aws.String(newRoleDescription),
		AssumeRolePolicyDocument: aws.String(trustPolicy),
	}

	roleOutput, err := iamClient.CreateRole(ctx, input)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create role %s", newRoleName)
	}

	for name, policy := range inlinePolicies {
		_, err := iamClient.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
			RoleName:       aws.String(newRoleName),
			PolicyDocument: aws.String(policy),
			PolicyName:     aws.String(name),
		})
		if err != nil {
			return "", errors.Wrapf(err, "error adding inline policy %s to role %s", name, newRoleName)
		}
	}

	for _, policyName := range managedPolicyNames {
		_, err := iamClient.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName:  aws.String(newRoleName),
			PolicyArn: aws.String(policyName),
		})
		if err != nil {
			return "", errors.Wrapf(err, "error attaching policy %s to role %s", policyName, newRoleName)
		}
	}

	return aws.ToString(roleOutput.Role.Arn), nil
}

const (
	trustedEntitiesEKS = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Effect": "Allow",
            "Principal": {
                "Service": "eks.amazonaws.com"
            },
            "Action": "sts:AssumeRole"
        }
    ]
}`
	trustedEntitiesEC2 = `{
  "Version": "2012-10-17",
  "Statement": [
      {
          "Effect": "Allow",
          "Principal": {
              "Service": "ec2.amazonaws.com"
          },
          "Action": "sts:AssumeRole"
      }
  ]
}`

	inlinePolicyCloudWatchMetrics = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Action": [
                "cloudwatch:PutMetricData"
            ],
            "Resource": "*",
            "Effect": "Allow"
        }
    ]
}`
	inlinePoliciesELBPermissions = `{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Action": [
                "ec2:DescribeAccountAttributes",
                "ec2:DescribeAddresses",
                "ec2:DescribeInternetGateways"
            ],
            "Resource": "*",
            "Effect": "Allow"
        }
    ]
}`
)
