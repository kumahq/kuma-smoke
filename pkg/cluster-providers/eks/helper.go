package eks

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/pkg/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	v1Prefix        = "k8s-aws-v1."
	clusterIDHeader = "x-k8s-aws-id"
)

func clientForCluster(ctx context.Context, awsCfg aws.Config, clusterName string) (*rest.Config, *kubernetes.Clientset, error) {
	eksClient := eks.NewFromConfig(awsCfg)
	stsClient := sts.NewFromConfig(awsCfg)

	// Fetch cluster details
	describeInput := &eks.DescribeClusterInput{
		Name: &clusterName,
	}
	resp, err := eksClient.DescribeCluster(ctx, describeInput)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to describe EKS cluster")
	}

	clusterInfo := resp.Cluster
	bearerToken, err := generateBearerToken(ctx, stsClient, clusterName)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to generate bearer token")
	}

	caData, err := base64.StdEncoding.DecodeString(*clusterInfo.CertificateAuthority.Data)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to decode certificate authority data")
	}
	// caller should parse env name from the output (.clusters[0].cluster.name)
	cfg := rest.Config{
		BearerToken: bearerToken,
		Host:        *clusterInfo.Endpoint,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: false,
			CAData:   caData,
		},
	}
	k, err := kubernetes.NewForConfig(&cfg)
	if err != nil {
		return nil, nil, err
	}

	return &cfg, k, nil
}

func generateBearerToken(ctx context.Context, stsClient *sts.Client, clusterID string) (string, error) {
	preSignClient := sts.NewPresignClient(stsClient)
	preSignURLRequest, err := preSignClient.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(presignOptions *sts.PresignOptions) {
		presignOptions.ClientOptions = append(presignOptions.ClientOptions, func(stsOptions *sts.Options) {
			stsOptions.APIOptions = append(stsOptions.APIOptions, smithyhttp.SetHeaderValue(clusterIDHeader, clusterID))
			stsOptions.APIOptions = append(stsOptions.APIOptions, smithyhttp.SetHeaderValue("X-Amz-Expires", "3600"))
			stsOptions.APIOptions = append(stsOptions.APIOptions, func(stack *middleware.Stack) error {
				_, err := stack.Build.Remove("UserAgent")
				return err
			})
		})
	})
	if err != nil {
		return "", err
	}

	token := fmt.Sprintf("%s.%s", v1Prefix, base64.RawURLEncoding.EncodeToString([]byte(preSignURLRequest.URL)))
	return token, nil
}
