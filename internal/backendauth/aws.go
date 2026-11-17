// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// awsHandler implements [Handler] for AWS Bedrock authz.
type awsHandler struct {
	credentialsProvider aws.CredentialsProvider
	signer              *v4.Signer
	region              string
}

// newAWSHandler returns the concrete type so awsCredentialOverrideHandler can reach signWith.
func newAWSHandler(ctx context.Context, awsAuth *filterapi.AWSAuth) (*awsHandler, error) {
	if awsAuth == nil {
		return nil, fmt.Errorf("aws auth configuration is required")
	}

	var cfg aws.Config
	var err error

	// If credentials file is provided, use it; otherwise use default credential chain
	// which automatically handles IRSA, EKS Pod Identity, instance roles, etc.
	if len(awsAuth.CredentialFileLiteral) != 0 {
		var tmpfile *os.File
		tmpfile, err = os.CreateTemp("", "aws-credentials")
		if err != nil {
			return nil, fmt.Errorf("cannot create temp file for AWS credentials: %w", err)
		}
		defer func() {
			_ = os.Remove(tmpfile.Name())
		}()
		if _, err = tmpfile.WriteString(awsAuth.CredentialFileLiteral); err != nil {
			return nil, fmt.Errorf("cannot write AWS credentials to temp file: %w", err)
		}
		name := tmpfile.Name()
		cfg, err = config.LoadDefaultConfig(
			ctx,
			config.WithSharedCredentialsFiles([]string{name}),
			config.WithRegion(awsAuth.Region),
		)
		if err != nil {
			return nil, fmt.Errorf("cannot load from credentials file: %w", err)
		}
	} else {
		// Use default credential chain (supports IRSA, EKS Pod Identity, etc.)
		cfg, err = config.LoadDefaultConfig(
			ctx,
			config.WithRegion(awsAuth.Region),
		)
		if err != nil {
			return nil, fmt.Errorf("cannot load AWS config: %w", err)
		}
	}

	signer := v4.NewSigner()

	return &awsHandler{credentialsProvider: cfg.Credentials, signer: signer, region: awsAuth.Region}, nil
}

// Do implements [Handler.Do].
//
// This assumes that during the transformation, the path is set in the header mutation as well as
// the body in the body mutation.
func (a *awsHandler) Do(ctx context.Context, requestHeaders map[string]string, mutatedBody []byte) ([]internalapi.Header, error) {
	credentials, err := a.credentialsProvider.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot retrieve AWS credentials: %w", err)
	}
	return a.signWith(ctx, &credentials, requestHeaders, mutatedBody)
}

// signWith signs with the given credentials, from either the handler's own provider or a
// per-request source. Only the credentials vary: host and region are resolved identically on both
// paths, so a per-request credential still gets VPC endpoint and region self-correction.
func (a *awsHandler) signWith(ctx context.Context, credentials *aws.Credentials, requestHeaders map[string]string, mutatedBody []byte) ([]internalapi.Header, error) {
	method := requestHeaders[":method"]
	path := requestHeaders[":path"]
	host := a.signingHost(requestHeaders)
	// Derive the SigV4 signing region from the resolved Bedrock host so a region/endpoint mismatch
	// (e.g. a us-east-1 VPCE under a us-west-2 configured region) self-corrects; non-Bedrock hosts keep
	// the configured region. This self-corrects only the signing scope — credential retrieval still uses
	// the region the handler was built with, which is fine for Bedrock (SigV4 credentials are global).
	region := a.region
	if r := internalapi.AWSBedrockRegionFromHost(host); r != "" {
		region = r
	}

	var body []byte
	if len(mutatedBody) > 0 {
		body = mutatedBody
	}

	payloadHash := sha256.Sum256(body)
	req, err := http.NewRequest(method,
		fmt.Sprintf("https://%s%s", host, path),
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("cannot create request: %w", err)
	}
	req.Host = host
	// By setting the content length to -1, we can avoid the inclusion of the `Content-Length` header in the signature.
	// https://github.com/aws/aws-sdk-go-v2/blob/755839b2eebb246c7eec79b65404aee105196d5b/aws/signer/v4/v4.go#L427-L431
	//
	// The reason why we want to avoid this is that the ExtProc filter will remove the content-length header
	// from the request currently. Envoy will instead do "transfer-encoding: chunked" for the request body,
	// which should be acceptable for AWS Bedrock or any modern HTTP service.
	// https://github.com/envoyproxy/envoy/blob/60b2b5187cf99db79ecfc54675354997af4765ea/source/extensions/filters/http/ext_proc/processor_state.cc#L180-L183
	req.ContentLength = -1

	err = a.signer.SignHTTP(ctx, *credentials, req,
		hex.EncodeToString(payloadHash[:]), "bedrock", region, time.Now())
	if err != nil {
		return nil, fmt.Errorf("cannot sign request: %w", err)
	}

	var headers []internalapi.Header
	for key, hdr := range req.Header {
		if key == "Authorization" || strings.HasPrefix(key, "X-Amz-") {
			headers = append(headers, internalapi.Header{key, hdr[0]})
			requestHeaders[key] = hdr[0]
		}
	}
	return headers, nil
}

// signingHost resolves the host used to compute the SigV4 signature. It prefers the host resolved at
// config-translation time and forwarded by the upstream ext_proc filter via
// [internalapi.UpstreamHostHeader], and falls back to the region-based default Bedrock host when that
// metadata is absent. It deliberately does not fall back to :authority or the host header: at signing
// time those carry the downstream gateway authority, not the upstream endpoint, so signing over them
// produces a silent 403.
func (a *awsHandler) signingHost(requestHeaders map[string]string) string {
	if host := requestHeaders[internalapi.UpstreamHostHeader]; host != "" {
		return host
	}
	return fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", a.region)
}
