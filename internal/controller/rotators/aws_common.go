// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package rotators provides credential rotation implementations.
//
// This file contains common AWS functionality shared between different AWS credential
// rotators. It provides:
//
// 1. AWS Client Interfaces and Implementations:
//   - STSClient for AWS STS API operations
//   - Concrete implementations with proper AWS SDK integration
//
// 2. Credential File Management:
//   - Parsing and formatting of AWS credentials file
//   - Handling of temporary credentials and session tokens
//
// 3. Common Configuration:
//   - Default AWS configuration with adaptive retry
//   - Standard timeouts and delays
//   - Session name formatting
package rotators

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	corev1 "k8s.io/api/core/v1"
)

// Common constants for AWS operations.
const (
	// awsSessionNameFormat is the format string for AWS session names.
	awsSessionNameFormat = "ai-gateway-%s"
	// AwsCredentialsKey is the key used to store AWS credentials in Kubernetes secrets.
	AwsCredentialsKey = "credentials"
)

// defaultAWSConfig returns an AWS config with adaptive retry mode enabled.
// This ensures better handling of transient API failures and rate limiting.
func defaultAWSConfig(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRetryMode(aws.RetryModeAdaptive),
	)
}

// STSClient defines the interface for AWS STS operations required by the rotators.
// This interface encapsulates the STS API operations needed for OIDC token exchange
// and role assumption.
type STSClient interface {
	// AssumeRoleWithWebIdentity exchanges a web identity token for temporary AWS credentials.
	AssumeRoleWithWebIdentity(ctx context.Context, params *sts.AssumeRoleWithWebIdentityInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error)
}

// stsClient implements the STSOperations interface using the AWS SDK v2.
// It provides a concrete implementation for STS operations using the official AWS SDK.
type stsClient struct {
	client *sts.Client
}

// NewSTSClient creates a new STSClient with the given AWS config.
// The client is configured with the provided AWS configuration, which should
// include appropriate credentials and region settings.
func NewSTSClient(cfg aws.Config) STSClient { //nolint:gocritic
	return &stsClient{
		client: sts.NewFromConfig(cfg),
	}
}

// AssumeRoleWithWebIdentity implements the STSOperations interface by exchanging
// a web identity token for temporary AWS credentials.
//
// This implements [STSClient.AssumeRoleWithWebIdentity].
func (c *stsClient) AssumeRoleWithWebIdentity(ctx context.Context, params *sts.AssumeRoleWithWebIdentityInput, optFns ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	return c.client.AssumeRoleWithWebIdentity(ctx, params, optFns...)
}

// awsCredentials represents an AWS credential including optional
// session token configuration. It maps to a single profile in an
// AWS credentials file.
type awsCredentials struct {
	// profile is the name of the credentials profile.
	profile string
	// accessKeyID is the AWS access key ID.
	accessKeyID string
	// secretAccessKey is the AWS secret access key.
	secretAccessKey string
	// sessionToken is the optional AWS session token for temporary credentials.
	sessionToken string
	// region is the optional AWS region for the profile.
	region string
}

// awsCredentialsFile represents a complete AWS credentials file containing a credential profile.
type awsCredentialsFile struct {
	// creds stores the aws credentials.
	creds awsCredentials
}

// formatAWSCredentialsFile formats an AWS credential profile into a credentials file.
// The output follows the standard AWS credentials file format and ensures:
//   - Proper formatting of all credential components
//   - Optional inclusion of session token
func formatAWSCredentialsFile(file *awsCredentialsFile) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[%s]\n", file.creds.profile)
	fmt.Fprintf(&builder, "aws_access_key_id = %s\n", file.creds.accessKeyID)
	fmt.Fprintf(&builder, "aws_secret_access_key = %s\n", file.creds.secretAccessKey)
	if file.creds.sessionToken != "" {
		fmt.Fprintf(&builder, "aws_session_token = %s\n", file.creds.sessionToken)
	}
	fmt.Fprintf(&builder, "region = %s\n", file.creds.region)

	return builder.String()
}

// updateAWSCredentialsInSecret updates AWS credentials in a secret.
func updateAWSCredentialsInSecret(secret *corev1.Secret, creds *awsCredentialsFile) {
	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}
	secret.Data[AwsCredentialsKey] = []byte(formatAWSCredentialsFile(creds))
}
