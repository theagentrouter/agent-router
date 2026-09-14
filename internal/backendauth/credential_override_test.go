// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"errors"
	"fmt"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// makeOverrideConfig returns a CredentialOverride using a request header source.
func makeHeaderOverride(headerName string, fallback bool) *filterapi.CredentialOverride {
	return &filterapi.CredentialOverride{
		HeaderName:           headerName,
		FallbackToConfigured: fallback,
		InputHeadersToRemove: []string{headerName},
	}
}

// makeMetadataOverride returns a CredentialOverride using dynamic metadata source.
func makeMetadataOverride(namespace, key string, fallback bool) *filterapi.CredentialOverride {
	return &filterapi.CredentialOverride{
		DynamicMetadataNamespace: namespace,
		DynamicMetadataKey:       key,
		FallbackToConfigured:     fallback,
	}
}

// metadataContext builds an Envoy MetadataContext with a single string value.
func metadataContext(namespace, key, value string) *corev3.Metadata {
	return &corev3.Metadata{
		FilterMetadata: map[string]*structpb.Struct{
			namespace: {
				Fields: map[string]*structpb.Value{
					key: structpb.NewStringValue(value),
				},
			},
		},
	}
}

func TestCredentialOverrideHandler_FromRequestHeaders(t *testing.T) {
	t.Run("header present uses override credential", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-api-key", true),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{"x-aigw-api-key": "per-request-key"}
		hdrs, err := h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer per-request-key", headers["Authorization"])
		require.Len(t, hdrs, 1)
		require.Equal(t, "Authorization", hdrs[0][0])
		require.Equal(t, "Bearer per-request-key", hdrs[0][1])
	})

	t.Run("header absent fallback=true uses static credential", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-api-key", true),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{}
		hdrs, err := h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer static-key", headers["Authorization"])
		require.Len(t, hdrs, 1)
	})

	t.Run("header absent fallback=false returns ErrCredentialMissing", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-api-key", false),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{}
		_, err = h.Do(t.Context(), headers, nil)
		require.ErrorIs(t, err, ErrCredentialMissing)
	})

	t.Run("header present strips whitespace", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-api-key", false),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{"x-aigw-api-key": "  my-key  "}
		_, err = h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer my-key", headers["Authorization"])
	})
}

func TestCredentialOverrideHandler_FromDynamicMetadata(t *testing.T) {
	const ns = "envoy.filters.http.ext_authz"
	const key = "upstream_api_key"

	t.Run("metadata present uses override credential", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeMetadataOverride(ns, key, true),
			applyFn: applyBearerCredential,
		}

		ctx := WithEnvoyMetadata(t.Context(), metadataContext(ns, key, "meta-key"))
		headers := map[string]string{}
		hdrs, err := h.Do(ctx, headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer meta-key", headers["Authorization"])
		require.Len(t, hdrs, 1)
	})

	t.Run("metadata absent fallback=true uses static credential", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeMetadataOverride(ns, key, true),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{}
		hdrs, err := h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer static-key", headers["Authorization"])
		require.Len(t, hdrs, 1)
	})

	t.Run("metadata absent fallback=false returns ErrCredentialMissing", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeMetadataOverride(ns, key, false),
			applyFn: applyBearerCredential,
		}

		_, err = h.Do(t.Context(), map[string]string{}, nil)
		require.ErrorIs(t, err, ErrCredentialMissing)
	})

	t.Run("no metadata context in ctx returns empty", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeMetadataOverride(ns, key, false),
			applyFn: applyBearerCredential,
		}

		// Context has no Envoy metadata — no WithEnvoyMetadata call.
		_, err = h.Do(t.Context(), map[string]string{}, nil)
		require.ErrorIs(t, err, ErrCredentialMissing)
	})

	t.Run("nil metadata context is safe", func(t *testing.T) {
		inner, err := newAPIKeyHandler(&filterapi.APIKeyAuth{Key: "static-key"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeMetadataOverride(ns, key, true),
			applyFn: applyBearerCredential,
		}

		ctx := WithEnvoyMetadata(t.Context(), nil)
		hdrs, err := h.Do(ctx, map[string]string{}, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer static-key", hdrs[0][1])
	})
}

func TestCredentialOverrideHandler_PerAuthType(t *testing.T) {
	t.Run("anthropic sets x-api-key", func(t *testing.T) {
		inner, err := newAnthropicAPIKeyHandler(&filterapi.AnthropicAPIKeyAuth{Key: "static"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-anthropic-api-key", false),
			applyFn: applyAnthropicCredential,
		}

		headers := map[string]string{"x-aigw-anthropic-api-key": "per-req"}
		_, err = h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "per-req", headers["x-api-key"])
	})

	t.Run("azure api key sets api-key", func(t *testing.T) {
		inner, err := newAzureAPIKeyHandler(&filterapi.AzureAPIKeyAuth{Key: "static"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-azure-api-key", false),
			applyFn: applyAzureAPIKeyCredential,
		}

		headers := map[string]string{"x-aigw-azure-api-key": "per-req"}
		_, err = h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "per-req", headers["api-key"])
	})

	t.Run("azure credentials sets Authorization Bearer", func(t *testing.T) {
		inner, err := newAzureHandler(&filterapi.AzureAuth{AccessToken: "static"})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-azure-access-token", false),
			applyFn: applyBearerCredential,
		}

		headers := map[string]string{"x-aigw-azure-access-token": "per-req-token"}
		_, err = h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer per-req-token", headers["Authorization"])
	})

	t.Run("gcp credentials sets Authorization Bearer and rewrites path", func(t *testing.T) {
		inner, err := newGCPHandler(t.Context(), &filterapi.GCPAuth{
			AccessToken: "static-token",
			Region:      "us-central1",
			ProjectName: "my-project",
		})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-gcp-access-token", false),
			applyFn: makeGCPApplyFn("us-central1", "my-project"),
		}

		headers := map[string]string{
			"x-aigw-gcp-access-token": "per-req-gcp-token",
			":path":                   "/models/gemini/generate",
		}
		hdrs, err := h.Do(t.Context(), headers, nil)
		require.NoError(t, err)
		require.Equal(t, "Bearer per-req-gcp-token", headers["Authorization"])
		// Double slash matches gcpHandler.Do() behaviour when :path starts with "/".
		require.Equal(t, "/v1/projects/my-project/locations/us-central1//models/gemini/generate", headers[":path"])
		require.Len(t, hdrs, 2)
	})

	t.Run("gcp missing :path returns error", func(t *testing.T) {
		inner, err := newGCPHandler(t.Context(), &filterapi.GCPAuth{
			AccessToken: "static-token",
			Region:      "us-central1",
			ProjectName: "my-project",
		})
		require.NoError(t, err)

		h := &credentialOverrideHandler{
			inner:   inner,
			config:  makeHeaderOverride("x-aigw-gcp-access-token", false),
			applyFn: makeGCPApplyFn("us-central1", "my-project"),
		}

		headers := map[string]string{"x-aigw-gcp-access-token": "per-req-gcp-token"}
		_, err = h.Do(t.Context(), headers, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), ":path")
	})
}

func TestNewHandler_WrapsWithOverride(t *testing.T) {
	t.Run("nil CredentialOverride returns plain handler", func(t *testing.T) {
		h, err := NewHandler(t.Context(), &filterapi.BackendAuth{
			APIKey: &filterapi.APIKeyAuth{Key: "k"},
		})
		require.NoError(t, err)
		_, ok := h.(*credentialOverrideHandler)
		require.False(t, ok, "no override configured — should not be wrapped")
	})

	t.Run("CredentialOverride wraps handler", func(t *testing.T) {
		h, err := NewHandler(t.Context(), &filterapi.BackendAuth{
			APIKey: &filterapi.APIKeyAuth{Key: "k"},
			CredentialOverride: &filterapi.CredentialOverride{
				HeaderName:           "x-aigw-api-key",
				FallbackToConfigured: true,
			},
		})
		require.NoError(t, err)
		_, ok := h.(*credentialOverrideHandler)
		require.True(t, ok, "override configured — handler should be wrapped")
	})
}

func TestErrCredentialMissing_IsSentinel(t *testing.T) {
	// errors.New does not wrap, so the sentinel is not detected.
	unrelated := errors.New("wrapped: " + ErrCredentialMissing.Error())
	require.NotErrorIs(t, unrelated, ErrCredentialMissing)

	// fmt.Errorf with %w wraps, so the sentinel IS detected.
	wrapped := fmt.Errorf("outer: %w", ErrCredentialMissing)
	require.ErrorIs(t, wrapped, ErrCredentialMissing)
}

// awsMetadataContext builds metadata holding a struct-valued AWS credential. An empty
// sessionToken is omitted, matching a producer with long-lived credentials.
func awsMetadataContext(namespace, key, accessKeyID, secretAccessKey, sessionToken string) *corev3.Metadata {
	fields := map[string]*structpb.Value{}
	if accessKeyID != "" {
		fields[awsMetadataAccessKeyIDField] = structpb.NewStringValue(accessKeyID)
	}
	if secretAccessKey != "" {
		fields[awsMetadataSecretAccessKeyField] = structpb.NewStringValue(secretAccessKey)
	}
	if sessionToken != "" {
		fields[awsMetadataSessionTokenField] = structpb.NewStringValue(sessionToken)
	}
	return &corev3.Metadata{
		FilterMetadata: map[string]*structpb.Struct{
			namespace: {Fields: map[string]*structpb.Value{key: structpb.NewStructValue(&structpb.Struct{Fields: fields})}},
		},
	}
}

// newAWSOverrideHandler builds a handler whose own chain resolves to the static values below, so
// tests tell override from fallback by the signed key ID.
func newAWSOverrideHandler(t *testing.T, config *filterapi.CredentialOverride) *awsCredentialOverrideHandler {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIASTATICFALLBACK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "static-fallback-secret")
	inner, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{Region: "us-east-1"})
	require.NoError(t, err)
	return &awsCredentialOverrideHandler{inner: inner, config: config}
}

// awsRequestHeaders returns the minimum signWith needs.
func awsRequestHeaders() map[string]string {
	return map[string]string{":method": "POST", ":path": "/model/anthropic.claude-v2/converse"}
}

func TestAWSCredentialOverrideHandler_FromRequestHeaders(t *testing.T) {
	const prefix = internalapi.AWSCredentialOverrideHeaderPrefix
	accessKeyIDHeader, secretAccessKeyHeader, sessionTokenHeader := internalapi.AWSCredentialOverrideHeaderNames(prefix)

	t.Run("all three headers present signs with the per-request credential", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, true))

		requestHeaders := awsRequestHeaders()
		requestHeaders[accessKeyIDHeader] = "ASIAPERREQUEST"
		requestHeaders[secretAccessKeyHeader] = "per-request-secret"
		requestHeaders[sessionTokenHeader] = "per-request-session-token"

		hdrs, err := h.Do(t.Context(), requestHeaders, []byte(`{"messages":[]}`))
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers["Authorization"], "Credential=ASIAPERREQUEST")
		require.NotContains(t, headers["Authorization"], "AKIASTATICFALLBACK")
		require.Equal(t, "per-request-session-token", headers["X-Amz-Security-Token"])
	})

	t.Run("no session token signs long-lived credentials", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, true))

		requestHeaders := awsRequestHeaders()
		requestHeaders[accessKeyIDHeader] = "AKIAPERREQUEST"
		requestHeaders[secretAccessKeyHeader] = "per-request-secret"

		hdrs, err := h.Do(t.Context(), requestHeaders, nil)
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers["Authorization"], "Credential=AKIAPERREQUEST")
		require.NotContains(t, headers, "X-Amz-Security-Token")
	})

	t.Run("custom prefix", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride("x-tenant-aws-", true))

		requestHeaders := awsRequestHeaders()
		requestHeaders["x-tenant-aws-access-key-id"] = "ASIACUSTOMPREFIX"
		requestHeaders["x-tenant-aws-secret-access-key"] = "custom-prefix-secret"

		hdrs, err := h.Do(t.Context(), requestHeaders, nil)
		require.NoError(t, err)
		require.Contains(t, stringPairsToMap(hdrs)["Authorization"], "Credential=ASIACUSTOMPREFIX")
	})

	// A partial credential means the filter upstream is misconfigured. These fail hard rather than
	// falling back, even with fallbackToConfigured=true.
	for _, tc := range []struct {
		name    string
		headers map[string]string
	}{
		{"access key id only", map[string]string{accessKeyIDHeader: "ASIAPARTIAL"}},
		{"secret access key only", map[string]string{secretAccessKeyHeader: "partial-secret"}},
		{"session token only", map[string]string{sessionTokenHeader: "orphan-session-token"}},
		{"session token and access key id", map[string]string{
			accessKeyIDHeader: "ASIAPARTIAL", sessionTokenHeader: "orphan-session-token",
		}},
	} {
		t.Run("incomplete credential: "+tc.name, func(t *testing.T) {
			h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, true))

			requestHeaders := awsRequestHeaders()
			for k, v := range tc.headers {
				requestHeaders[k] = v
			}

			_, err := h.Do(t.Context(), requestHeaders, nil)
			require.ErrorIs(t, err, ErrIncompleteAWSCredential)
		})
	}

	t.Run("whitespace-only header counts as absent", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, true))

		requestHeaders := awsRequestHeaders()
		requestHeaders[accessKeyIDHeader] = "   "
		requestHeaders[secretAccessKeyHeader] = ""

		hdrs, err := h.Do(t.Context(), requestHeaders, nil)
		require.NoError(t, err)
		require.Contains(t, stringPairsToMap(hdrs)["Authorization"], "Credential=AKIASTATICFALLBACK")
	})

	t.Run("headers absent falls back to the configured chain", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, true))

		hdrs, err := h.Do(t.Context(), awsRequestHeaders(), nil)
		require.NoError(t, err)
		require.Contains(t, stringPairsToMap(hdrs)["Authorization"], "Credential=AKIASTATICFALLBACK")
	})

	t.Run("headers absent without fallback returns ErrCredentialMissing", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeHeaderOverride(prefix, false))

		_, err := h.Do(t.Context(), awsRequestHeaders(), nil)
		require.ErrorIs(t, err, ErrCredentialMissing)
	})
}

func TestAWSCredentialOverrideHandler_FromDynamicMetadata(t *testing.T) {
	const (
		namespace = "envoy.filters.http.ext_authz"
		key       = internalapi.AWSCredentialOverrideMetadataKey
	)

	t.Run("struct value signs with the per-request credential", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, true))
		ctx := WithEnvoyMetadata(t.Context(),
			awsMetadataContext(namespace, key, "ASIAFROMMETADATA", "metadata-secret", "metadata-session-token"))

		hdrs, err := h.Do(ctx, awsRequestHeaders(), []byte(`{"messages":[]}`))
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers["Authorization"], "Credential=ASIAFROMMETADATA")
		require.Equal(t, "metadata-session-token", headers["X-Amz-Security-Token"])
	})

	t.Run("no session token field signs long-lived credentials", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, true))
		ctx := WithEnvoyMetadata(t.Context(),
			awsMetadataContext(namespace, key, "AKIAFROMMETADATA", "metadata-secret", ""))

		hdrs, err := h.Do(ctx, awsRequestHeaders(), nil)
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers["Authorization"], "Credential=AKIAFROMMETADATA")
		require.NotContains(t, headers, "X-Amz-Security-Token")
	})

	t.Run("incomplete struct fails hard", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, true))
		ctx := WithEnvoyMetadata(t.Context(),
			awsMetadataContext(namespace, key, "ASIAPARTIAL", "", ""))

		_, err := h.Do(ctx, awsRequestHeaders(), nil)
		require.ErrorIs(t, err, ErrIncompleteAWSCredential)
	})

	t.Run("no metadata at all falls back", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, true))

		hdrs, err := h.Do(t.Context(), awsRequestHeaders(), nil)
		require.NoError(t, err)
		require.Contains(t, stringPairsToMap(hdrs)["Authorization"], "Credential=AKIASTATICFALLBACK")
	})

	t.Run("wrong namespace falls back", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, true))
		ctx := WithEnvoyMetadata(t.Context(),
			awsMetadataContext("some.other.filter", key, "ASIAFROMMETADATA", "metadata-secret", ""))

		hdrs, err := h.Do(ctx, awsRequestHeaders(), nil)
		require.NoError(t, err)
		require.Contains(t, stringPairsToMap(hdrs)["Authorization"], "Credential=AKIASTATICFALLBACK")
	})

	// A string where a struct belongs is a producer bug, but indistinguishable from an absent
	// value without a type check that would have to fail somewhere. It takes the absent path, so
	// fallbackToConfigured decides: set it false to surface the misconfiguration as a 401.
	t.Run("string value instead of struct takes the absent path", func(t *testing.T) {
		h := newAWSOverrideHandler(t, makeMetadataOverride(namespace, key, false))
		ctx := WithEnvoyMetadata(t.Context(), metadataContext(namespace, key, "not-a-struct"))

		_, err := h.Do(ctx, awsRequestHeaders(), nil)
		require.ErrorIs(t, err, ErrCredentialMissing)
	})

	t.Run("metadata source strips no headers", func(t *testing.T) {
		// The credential never touches the request, so nothing to remove.
		require.Empty(t, makeMetadataOverride(namespace, key, true).InputHeadersToRemove)
	})
}

func TestNewHandler_AWSCredentialOverride(t *testing.T) {
	t.Run("no override returns the plain AWS handler", func(t *testing.T) {
		h, err := NewHandler(t.Context(), &filterapi.BackendAuth{
			AWSAuth: &filterapi.AWSAuth{Region: "us-east-1"},
		})
		require.NoError(t, err)
		require.IsType(t, &awsHandler{}, h)
	})

	t.Run("override wraps the AWS handler", func(t *testing.T) {
		h, err := NewHandler(t.Context(), &filterapi.BackendAuth{
			AWSAuth:            &filterapi.AWSAuth{Region: "us-east-1"},
			CredentialOverride: makeHeaderOverride(internalapi.AWSCredentialOverrideHeaderPrefix, true),
		})
		require.NoError(t, err)
		require.IsType(t, &awsCredentialOverrideHandler{}, h)
	})
}

func TestErrIncompleteAWSCredential_IsSentinel(t *testing.T) {
	require.ErrorIs(t, fmt.Errorf("outer: %w", ErrIncompleteAWSCredential), ErrIncompleteAWSCredential)
}
