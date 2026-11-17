// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// ErrCredentialMissing is returned when the per-request credential source is configured,
// the source is absent, and FallbackToConfigured is false.
// ProcessRequestHeaders converts this to a 401 ImmediateResponse.
var ErrCredentialMissing = errors.New("per-request credential missing")

// envoyMetadataKey is the context key used to thread Envoy's MetadataContext into handler Do() calls.
type envoyMetadataKey struct{}

// WithEnvoyMetadata stores the Envoy dynamic metadata context so the credential override
// handler can read it without changing the BackendAuthHandler interface.
// When md is nil (Envoy sent no metadata), ctx is returned unchanged so that
// envoyMetadataFromContext still returns nil — the handler then follows its fallback path.
func WithEnvoyMetadata(ctx context.Context, md *corev3.Metadata) context.Context {
	if md == nil {
		return ctx
	}
	return context.WithValue(ctx, envoyMetadataKey{}, md)
}

// envoyMetadataFromContext retrieves the Envoy metadata stored by WithEnvoyMetadata.
func envoyMetadataFromContext(ctx context.Context) *corev3.Metadata {
	md, _ := ctx.Value(envoyMetadataKey{}).(*corev3.Metadata)
	return md
}

// applyCredentialFn sets the appropriate output header(s) for a given auth type
// using the supplied per-request credential string.
type applyCredentialFn func(requestHeaders map[string]string, credential string) ([]internalapi.Header, error)

// applyBearerCredential sets Authorization: Bearer <credential>.
func applyBearerCredential(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
	v := fmt.Sprintf("Bearer %s", credential)
	requestHeaders["Authorization"] = v
	return []internalapi.Header{{"Authorization", v}}, nil
}

// applyAnthropicCredential sets x-api-key: <credential>.
func applyAnthropicCredential(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
	requestHeaders["x-api-key"] = credential
	return []internalapi.Header{{"x-api-key", credential}}, nil
}

// applyAzureAPIKeyCredential sets api-key: <credential>.
func applyAzureAPIKeyCredential(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
	requestHeaders["api-key"] = credential
	return []internalapi.Header{{"api-key", credential}}, nil
}

// makeGCPApplyFn returns an applyCredentialFn for GCP that also rewrites the :path header
// with the region and project prefix, matching the behaviour of gcpHandler.Do().
// Note: like gcpHandler.Do, the resulting path has a double slash when the original :path
// starts with "/" (e.g. "/v1/projects/.../region//models/..."). This is intentional parity.
func makeGCPApplyFn(region, projectName string) applyCredentialFn {
	return func(requestHeaders map[string]string, credential string) ([]internalapi.Header, error) {
		path := requestHeaders[":path"]
		if path == "" {
			return nil, fmt.Errorf("missing ':path' header in the request")
		}
		prefixPath := fmt.Sprintf("/v1/projects/%s/locations/%s", projectName, region)
		newPath := fmt.Sprintf("%s/%s", prefixPath, path)
		requestHeaders[":path"] = newPath
		v := fmt.Sprintf("Bearer %s", credential)
		requestHeaders["Authorization"] = v
		return []internalapi.Header{{":path", newPath}, {"Authorization", v}}, nil
	}
}

// credentialOverrideHandler wraps an inner BackendAuthHandler and sources the upstream
// credential per-request when BackendAuth.CredentialOverride is configured.
// When the per-request source is absent and FallbackToConfigured is true, it delegates
// to the inner handler. When FallbackToConfigured is false, it returns ErrCredentialMissing.
type credentialOverrideHandler struct {
	inner   filterapi.BackendAuthHandler
	config  *filterapi.CredentialOverride
	applyFn applyCredentialFn
}

// Do implements filterapi.BackendAuthHandler.
func (h *credentialOverrideHandler) Do(ctx context.Context, requestHeaders map[string]string, mutatedBody []byte) ([]internalapi.Header, error) {
	credential := h.resolveCredential(ctx, requestHeaders)
	if credential == "" {
		if !h.config.FallbackToConfigured {
			return nil, ErrCredentialMissing
		}
		return h.inner.Do(ctx, requestHeaders, mutatedBody)
	}
	return h.applyFn(requestHeaders, credential)
}

// resolveCredential reads the per-request credential from the configured source.
// Returns an empty string when the source is absent.
func (h *credentialOverrideHandler) resolveCredential(ctx context.Context, requestHeaders map[string]string) string {
	o := h.config
	if o.HeaderName != "" {
		return strings.TrimSpace(requestHeaders[o.HeaderName])
	}
	if o.DynamicMetadataNamespace != "" {
		return strings.TrimSpace(lookupMetadataValue(ctx, o).GetStringValue())
	}
	return ""
}

// lookupMetadataValue returns the value at the configured namespace/key, or nil if absent.
// structpb getters are nil-safe, so callers can chain off the result.
func lookupMetadataValue(ctx context.Context, o *filterapi.CredentialOverride) *structpb.Value {
	md := envoyMetadataFromContext(ctx)
	if md == nil {
		return nil
	}
	ns, ok := md.GetFilterMetadata()[o.DynamicMetadataNamespace]
	if !ok || ns == nil {
		return nil
	}
	val, ok := ns.GetFields()[o.DynamicMetadataKey]
	if !ok {
		return nil
	}
	return val
}

// Fields of the metadata struct carrying an AWS credential. Fixed, not configurable: the shape is
// the contract with whatever filter produces it.
const (
	awsMetadataAccessKeyIDField     = "accessKeyId"
	awsMetadataSecretAccessKeyField = "secretAccessKey"
	awsMetadataSessionTokenField    = "sessionToken"
)

// awsCredentialSource marks credentials as per-request in AWS SDK errors.
const awsCredentialSource = "AIGatewayCredentialOverride"

// ErrIncompleteAWSCredential means the source carried some but not all of the SigV4 inputs.
// Failing beats falling back: the filter upstream is misconfigured, and signing with the gateway's
// own identity would attribute the request to the wrong principal.
var ErrIncompleteAWSCredential = errors.New("incomplete per-request AWS credential: access key ID and secret access key must both be present")

// awsCredentialOverrideHandler is the AWS counterpart to credentialOverrideHandler. SigV4 consumes
// three values and signs, so it does not fit applyCredentialFn.
type awsCredentialOverrideHandler struct {
	inner  *awsHandler
	config *filterapi.CredentialOverride
}

// Do implements filterapi.BackendAuthHandler.
func (h *awsCredentialOverrideHandler) Do(ctx context.Context, requestHeaders map[string]string, mutatedBody []byte) ([]internalapi.Header, error) {
	credentials, err := h.resolveAWSCredentials(ctx, requestHeaders)
	if err != nil {
		return nil, err
	}
	if credentials == nil {
		if !h.config.FallbackToConfigured {
			return nil, ErrCredentialMissing
		}
		// Configured credential chain: credentials file, IRSA, Pod Identity, instance role.
		return h.inner.Do(ctx, requestHeaders, mutatedBody)
	}
	return h.inner.signWith(ctx, credentials, requestHeaders, mutatedBody)
}

// resolveAWSCredentials reads the three-part SigV4 credential from the configured source.
// Returns nil, nil when the source is absent, selecting the fallback path.
func (h *awsCredentialOverrideHandler) resolveAWSCredentials(ctx context.Context, requestHeaders map[string]string) (*aws.Credentials, error) {
	var accessKeyID, secretAccessKey, sessionToken string

	o := h.config
	switch {
	case o.HeaderName != "":
		// HeaderName is a prefix here, not a full header name. The controller builds its strip
		// list from the same function.
		accessKeyIDHeader, secretAccessKeyHeader, sessionTokenHeader := internalapi.AWSCredentialOverrideHeaderNames(o.HeaderName)
		accessKeyID = strings.TrimSpace(requestHeaders[accessKeyIDHeader])
		secretAccessKey = strings.TrimSpace(requestHeaders[secretAccessKeyHeader])
		sessionToken = strings.TrimSpace(requestHeaders[sessionTokenHeader])
	case o.DynamicMetadataNamespace != "":
		fields := lookupMetadataValue(ctx, o).GetStructValue().GetFields()
		accessKeyID = strings.TrimSpace(fields[awsMetadataAccessKeyIDField].GetStringValue())
		secretAccessKey = strings.TrimSpace(fields[awsMetadataSecretAccessKeyField].GetStringValue())
		sessionToken = strings.TrimSpace(fields[awsMetadataSessionTokenField].GetStringValue())
	default:
		return nil, nil
	}

	// Nothing at all: fall back, or 401.
	if accessKeyID == "" && secretAccessKey == "" && sessionToken == "" {
		return nil, nil
	}
	// Something, but not enough to sign. A lone session token counts as partial.
	if accessKeyID == "" || secretAccessKey == "" {
		return nil, ErrIncompleteAWSCredential
	}

	return &aws.Credentials{
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		SessionToken:    sessionToken,
		Source:          awsCredentialSource,
	}, nil
}
