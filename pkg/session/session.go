package session

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"k8s.io/klog/v2"
)

func Create(ctx context.Context, version string) *session.Session {
	sess := withUserAgent(session.Must(session.NewSession(
		request.WithRetryer(
			&aws.Config{STSRegionalEndpoint: endpoints.RegionalSTSEndpoint},
			client.DefaultRetryer{NumMaxRetries: client.DefaultRetryerMaxNumRetries},
		),
	)), version)

	if *sess.Config.Region == "" {
		klog.Infof("AWS region not configured, asking EC2 Instance Metadata Service")
		*sess.Config.Region = getRegionFromIMDS(ctx, sess)
	}
	klog.Infof("Using AWS region %s", *sess.Config.Region)
	return sess
}

// withUserAgent adds a karpenter specific user-agent string to AWS session
func withUserAgent(sess *session.Session, version string) *session.Session {
	userAgent := fmt.Sprintf("lambda-link-%s", version)
	sess.Handlers.Build.PushBack(request.MakeAddToUserAgentFreeFormHandler(userAgent))
	return sess
}

// get the current region from EC2 IMDS
func getRegionFromIMDS(ctx context.Context, sess *session.Session) string {
	region, err := ec2metadata.New(sess).RegionWithContext(ctx)
	if err != nil {
		panic(fmt.Sprintf("Failed to call the metadata server's region API, %s", err))
	}
	return region
}
