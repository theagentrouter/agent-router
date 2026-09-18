// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package rotators

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/envoyproxy/ai-gateway/internal/controller/tokenprovider"
)

// AWSOIDCRotator implements the Rotator interface for AWS OIDC token exchange.
// It manages the lifecycle of temporary AWS credentials obtained through OIDC token
// exchange with AWS STS.
type AWSOIDCRotator struct {
	// client is used for Kubernetes API operations.
	client client.Client
	// kube provides additional Kubernetes API capabilities.
	kube kubernetes.Interface
	// logger is used for structured logging.
	logger logr.Logger
	// stsClient provides AWS STS operations interface.
	stsClient STSClient
	// backendSecurityPolicyName provides name of backend security policy.
	backendSecurityPolicyName string
	// backendSecurityPolicyNamespace provides namespace of backend security policy.
	backendSecurityPolicyNamespace string
	// preRotationWindow specifies how long before expiry to rotate.
	preRotationWindow time.Duration
	oidc              *egv1a1.OIDC
	// roleArn is the role ARN used to obtain credentials.
	roleArn string
	// region is the AWS region for the credentials.
	region string
}

// NewAWSOIDCRotator creates a new AWS OIDC rotator with the specified configuration.
// It initializes the AWS STS client and sets up the rotation channels.
func NewAWSOIDCRotator(
	ctx context.Context,
	client client.Client,
	stsClient STSClient,
	kube kubernetes.Interface,
	logger logr.Logger,
	backendSecurityPolicyNamespace string,
	backendSecurityPolicyName string,
	preRotationWindow time.Duration,
	oidc *egv1a1.OIDC,
	roleArn string,
	region string,
) (*AWSOIDCRotator, error) {
	cfg, err := defaultAWSConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	cfg.Region = region
	if proxyURL := os.Getenv("AI_GATEWAY_STS_PROXY_URL"); proxyURL != "" {
		cfg.HTTPClient = &http.Client{
			Transport: &http.Transport{
				Proxy: func(*http.Request) (*url.URL, error) {
					return url.Parse(proxyURL)
				},
			},
		}
	}
	if stsClient == nil {
		stsClient = NewSTSClient(cfg)
	}
	return &AWSOIDCRotator{
		client:                         client,
		kube:                           kube,
		logger:                         logger.WithName("aws-oidc-rotator"),
		stsClient:                      stsClient,
		backendSecurityPolicyNamespace: backendSecurityPolicyNamespace,
		backendSecurityPolicyName:      backendSecurityPolicyName,
		preRotationWindow:              preRotationWindow,
		oidc:                           oidc,
		roleArn:                        roleArn,
		region:                         region,
	}, nil
}

// IsExpired checks if the preRotation time is before the current time.
func (r *AWSOIDCRotator) IsExpired(preRotationExpirationTime time.Time) bool {
	return IsBufferedTimeExpired(0, preRotationExpirationTime)
}

// GetPreRotationTime gets the expiration time minus the preRotation interval or return zero value for time.
func (r *AWSOIDCRotator) GetPreRotationTime(ctx context.Context) (time.Time, error) {
	secret, err := LookupSecret(ctx, r.client, r.backendSecurityPolicyNamespace, GetBSPSecretName(r.backendSecurityPolicyName))
	if err != nil {
		// return zero value for time if secret has not been created.
		if apierrors.IsNotFound(err) {
			return time.Time{}, err
		}
		return time.Time{}, err
	}
	expirationTime, err := GetExpirationSecretAnnotation(secret)
	if err != nil {
		return time.Time{}, err
	}
	preRotationTime := expirationTime.Add(-r.preRotationWindow)
	return preRotationTime, nil
}

// populateSecretWithAwsIdentity populates secret with aws identity credential info e.g. expiration time, access key, secret key and session token.
func populateSecretWithAwsIdentity(secret *corev1.Secret, awsIdentity *sts.AssumeRoleWithWebIdentityOutput, region string) {
	updateExpirationSecretAnnotation(secret, *awsIdentity.Credentials.Expiration)
	// For now have profile as default.
	const defaultProfile = "default"
	credsFile := awsCredentialsFile{awsCredentials{
		profile:         defaultProfile,
		accessKeyID:     aws.ToString(awsIdentity.Credentials.AccessKeyId),
		secretAccessKey: aws.ToString(awsIdentity.Credentials.SecretAccessKey),
		sessionToken:    aws.ToString(awsIdentity.Credentials.SessionToken),
		region:          region,
	}}
	updateAWSCredentialsInSecret(secret, &credsFile)
}

// Rotate implements aws credential secret upsert operation to k8s secret store.
//
// This implements [Rotator.Rotate].
func (r *AWSOIDCRotator) Rotate(ctx context.Context) (time.Time, error) {
	bspNamespace := r.backendSecurityPolicyNamespace
	bspName := r.backendSecurityPolicyName
	secretName := GetBSPSecretName(bspName)

	r.logger.Info("rotating aws credentials secret", "namespace", bspNamespace, "name", bspName)
	// TODO  move provider as part of constructor to make mock test possible when implement Azure OIDC.
	oidcProvider, err := tokenprovider.NewOidcTokenProvider(ctx, r.client, r.oidc)
	if err != nil {
		r.logger.Error(err, "failed to construct oidc provider")
		return time.Time{}, err
	}
	accessToken, err := oidcProvider.GetToken(ctx)
	if err != nil {
		r.logger.Error(err, "failed to get token from oidc provider", "oidcIssuer", r.oidc.Provider.Issuer)
		return time.Time{}, err
	}
	awsIdentity, err := r.assumeRoleWithToken(ctx, accessToken.Token)

	if err != nil {
		r.logger.Error(err, "failed to assume role", "role", r.roleArn)
		return time.Time{}, err
	} else if awsIdentity.Credentials == nil {
		return time.Time{}, fmt.Errorf("unexpected nil awsIdentity credentials for %s in %s", bspName, bspNamespace)
	}
	r.logger.Info(fmt.Sprintf("awsIdentity Credentials will expire in '%s'", awsIdentity.Credentials.Expiration.String()), "namespace", bspNamespace, "name", bspName)

	secret, err := LookupSecret(ctx, r.client, bspNamespace, secretName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			r.logger.Info("creating a new aws credentials secret", "namespace", bspNamespace, "name", bspName)
			secret = &corev1.Secret{
				Name:      secretName,
				Namespace: bspNamespace,
				Type:      corev1.SecretTypeOpaque,
				Data:      make(map[string][]byte),
			}
			populateSecretWithAwsIdentity(secret, awsIdentity, r.region)
			return *awsIdentity.Credentials.Expiration, r.client.Create(ctx, secret)
		}
		r.logger.Error(err, "failed to lookup aws credentials secret", "namespace", bspNamespace, "name", bspName)
		return time.Time{}, err
	}
	r.logger.Info("updating existing aws credential secret", "namespace", bspNamespace, "name", bspName)
	populateSecretWithAwsIdentity(secret, awsIdentity, r.region)
	return *awsIdentity.Credentials.Expiration, r.client.Update(ctx, secret)
}

// assumeRoleWithToken exchanges an OIDC token for AWS credentials.
func (r *AWSOIDCRotator) assumeRoleWithToken(ctx context.Context, token string) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	return r.stsClient.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          aws.String(r.roleArn),
		WebIdentityToken: aws.String(token),
		RoleSessionName:  aws.String(fmt.Sprintf(awsSessionNameFormat, r.backendSecurityPolicyName)),
	})
}
