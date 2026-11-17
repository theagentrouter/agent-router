// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func stringPairsToMap(pairs []internalapi.Header) map[string]string {
	result := make(map[string]string)
	for _, h := range pairs {
		result[h.Key()] = h.Value()
	}
	return result
}

func TestNewAWSHandler(t *testing.T) {
	t.Run("credentials file", func(t *testing.T) {
		awsFileBody := "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			CredentialFileLiteral: awsFileBody,
			Region:                "us-east-1",
		})
		require.NoError(t, err)
		require.NotNil(t, handler)

		require.Equal(t, "us-east-1", handler.region)
		require.NotNil(t, handler.credentialsProvider)
		require.NotNil(t, handler.signer)
	})

	t.Run("default credential chain with environment variables", func(t *testing.T) {
		// Set temporary environment variables for testing
		t.Setenv("AWS_ACCESS_KEY_ID", "test-key-id")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")

		// Note: AWS SDK's default credential chain will try multiple sources:
		// 1. Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
		// 2. Web identity token (IRSA) - AWS_ROLE_ARN, AWS_WEB_IDENTITY_TOKEN_FILE
		// 3. EKS Pod Identity
		// 4. EC2 instance metadata
		// 5. Shared credentials file
		//
		// This test validates the default credential chain works with environment variables
		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			Region: "us-west-2",
		})
		require.NoError(t, err)
		require.NotNil(t, handler)

		require.Equal(t, "us-west-2", handler.region)

		// Verify credentials can be retrieved from environment
		creds, err := handler.credentialsProvider.Retrieve(t.Context())
		require.NoError(t, err)
		require.Equal(t, "test-key-id", creds.AccessKeyID)
		require.Equal(t, "test-secret-key", creds.SecretAccessKey)
	})

	t.Run("default credential chain without credentials", func(t *testing.T) {
		// Clear AWS environment variables to ensure no credentials are available
		t.Setenv("AWS_ACCESS_KEY_ID", "")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "")
		t.Setenv("AWS_SESSION_TOKEN", "")
		t.Setenv("AWS_PROFILE", "")
		t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/dev/null")
		t.Setenv("AWS_CONFIG_FILE", "/dev/null")
		t.Setenv("AWS_ROLE_ARN", "")
		t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "")

		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			Region: "us-east-1",
		})
		// Handler creation should succeed even without credentials
		// (credentials are retrieved lazily at signing time)
		require.NoError(t, err)
		require.NotNil(t, handler)

		// But calling Do() should fail when no credentials are available
		_, err = handler.Do(t.Context(), map[string]string{":method": "POST", ":path": "/model/test/converse"}, []byte(`{"test": "data"}`))
		require.Error(t, err)
		require.Contains(t, err.Error(), "cannot retrieve AWS credentials")
	})

	t.Run("nil config", func(t *testing.T) {
		handler, err := newAWSHandler(t.Context(), nil)
		require.Error(t, err)
		require.Nil(t, handler)
		require.Contains(t, err.Error(), "aws auth configuration is required")
	})
}

func TestAWSHandler_SigningHost(t *testing.T) {
	handler := &awsHandler{region: "us-east-1"}
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "signing host header is used",
			headers: map[string]string{internalapi.UpstreamHostHeader: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"},
			want:    "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com",
		},
		{
			name: "authority and host are ignored, falls to region default",
			headers: map[string]string{
				":authority": "gateway.example.com",
				"host":       "gateway.example.com",
			},
			want: "bedrock-runtime.us-east-1.amazonaws.com",
		},
		{
			name:    "default host when no metadata",
			headers: map[string]string{},
			want:    "bedrock-runtime.us-east-1.amazonaws.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, handler.signingHost(tc.headers))
		})
	}
}

// TestAWSHandler_Do_SignsOverResolvedHost asserts the SigV4 signature is actually computed over the
// resolved signing host (and region), by independently recomputing the Authorization header and asserting
// byte-equality. It is credential-free (static keys), so fork CI can run it.
func TestAWSHandler_Do_SignsOverResolvedHost(t *testing.T) {
	const awsFileBody = "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
	// The independent recompute must use the same static credentials the handler loads from the file.
	refCreds := aws.Credentials{AccessKeyID: "test", SecretAccessKey: "secret"}
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	const path = "/model/anthropic.claude-v2/converse"

	newHandler := func(t *testing.T) *awsHandler {
		h, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{CredentialFileLiteral: awsFileBody, Region: "us-east-1"})
		require.NoError(t, err)
		return h
	}

	// recompute reproduces the handler's request construction and signing for a known host/region using
	// the timestamp the handler actually used (its X-Amz-Date), and returns the Authorization header.
	recompute := func(t *testing.T, host, region, amzDate string) string {
		ts, err := time.Parse("20060102T150405Z", amzDate)
		require.NoError(t, err)
		payloadHash := sha256.Sum256(body)
		req, err := http.NewRequest(http.MethodPost, "https://"+host+path, bytes.NewReader(body))
		require.NoError(t, err)
		req.Host = host
		req.ContentLength = -1
		require.NoError(t, v4.NewSigner().SignHTTP(t.Context(), refCreds, req,
			hex.EncodeToString(payloadHash[:]), "bedrock", region, ts))
		return req.Header.Get("Authorization")
	}

	// sign runs the real handler path and returns the resulting Authorization + X-Amz-Date.
	sign := func(t *testing.T, extra map[string]string) (auth, amzDate string) {
		headers := map[string]string{":method": http.MethodPost, ":path": path}
		maps.Copy(headers, extra)
		_, err := newHandler(t).Do(t.Context(), headers, body)
		require.NoError(t, err)
		return headers["Authorization"], headers["X-Amz-Date"]
	}

	t.Run("signs over the VPCE host from metadata", func(t *testing.T) {
		const vpce = "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"
		auth, amzDate := sign(t, map[string]string{internalapi.UpstreamHostHeader: vpce})
		require.Equal(t, recompute(t, vpce, "us-east-1", amzDate), auth)
	})

	t.Run("signs over the region default when metadata absent", func(t *testing.T) {
		auth, amzDate := sign(t, nil)
		require.Equal(t, recompute(t, "bedrock-runtime.us-east-1.amazonaws.com", "us-east-1", amzDate), auth)
	})

	t.Run("resolved host changes the signature", func(t *testing.T) {
		vpceAuth, _ := sign(t, map[string]string{internalapi.UpstreamHostHeader: "vpce-123.bedrock-runtime.us-east-1.vpce.amazonaws.com"})
		defaultAuth, _ := sign(t, nil)
		require.NotEqual(t, defaultAuth, vpceAuth)
	})

	t.Run("signing region self-corrects from the resolved host", func(t *testing.T) {
		// Credential scope uses us-west-2 (derived from the host), not the configured us-east-1.
		const vpce = "vpce-123.bedrock-runtime.us-west-2.vpce.amazonaws.com"
		auth, amzDate := sign(t, map[string]string{internalapi.UpstreamHostHeader: vpce})
		require.Contains(t, auth, "/us-west-2/bedrock/aws4_request")
		require.Equal(t, recompute(t, vpce, "us-west-2", amzDate), auth)
	})

	t.Run("signing region self-corrects from a FIPS resolved host", func(t *testing.T) {
		const fips = "bedrock-runtime-fips.us-west-2.amazonaws.com"
		auth, amzDate := sign(t, map[string]string{internalapi.UpstreamHostHeader: fips})
		require.Contains(t, auth, "/us-west-2/bedrock/aws4_request")
		require.Equal(t, recompute(t, fips, "us-west-2", amzDate), auth)
	})

	t.Run("signing region self-corrects from an api.aws resolved host", func(t *testing.T) {
		const apiAWS = "bedrock-runtime.us-west-2.api.aws"
		auth, amzDate := sign(t, map[string]string{internalapi.UpstreamHostHeader: apiAWS})
		require.Contains(t, auth, "/us-west-2/bedrock/aws4_request")
		require.Equal(t, recompute(t, apiAWS, "us-west-2", amzDate), auth)
	})

	t.Run("configured region is kept for a custom host with no derivable region", func(t *testing.T) {
		const custom = "bedrock.corp.internal"
		auth, amzDate := sign(t, map[string]string{internalapi.UpstreamHostHeader: custom})
		require.Contains(t, auth, "/us-east-1/bedrock/aws4_request")
		require.Equal(t, recompute(t, custom, "us-east-1", amzDate), auth)
	})
}

func TestAWSHandler_Do(t *testing.T) {
	t.Run("concurrent signing with credentials file", func(t *testing.T) {
		awsFileBody := "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			CredentialFileLiteral: awsFileBody,
			Region:                "us-east-1",
		})
		require.NoError(t, err)

		// Handler.Do is called concurrently, so we test it with 100 goroutines to ensure it is thread-safe.
		var wg sync.WaitGroup
		wg.Add(100)
		for range 100 {
			go func() {
				defer wg.Done()
				hdrs, err := handler.Do(t.Context(), map[string]string{":method": "POST", ":path": "/model/some-random-model/converse"}, []byte(`{"messages": [{"role": "user", "content": [{"text": "Say this is a test!"}]}]}`))
				require.NoError(t, err)

				headers := stringPairsToMap(hdrs)
				require.Contains(t, headers, "X-Amz-Date")
				require.Contains(t, headers, "Authorization")
			}()
		}
		wg.Wait()
	})

	t.Run("default credential chain with static env vars", func(t *testing.T) {
		// Test the default credential chain with basic static credentials
		// This validates IRSA/Pod Identity mechanism (credentials from environment)
		t.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")

		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			Region: "us-east-1",
		})
		require.NoError(t, err)

		hdrs, err := handler.Do(t.Context(), map[string]string{
			":method": "POST", ":path": "/model/amazon.titan-text-express-v1/invoke",
		}, []byte(`{"inputText": "Hello from default chain"}`))
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers, "X-Amz-Date")
		require.Contains(t, headers, "Authorization")
		// Verify the authorization header contains the access key ID
		require.Contains(t, headers["Authorization"], "Credential=AKIAIOSFODNN7EXAMPLE")
	})

	t.Run("session token in headers", func(t *testing.T) {
		// Test that session tokens (temporary credentials from STS/IRSA) are properly included
		t.Setenv("AWS_ACCESS_KEY_ID", "ASIATESTACCESSKEY")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
		t.Setenv("AWS_SESSION_TOKEN", "temporary-session-token-xyz")

		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			Region: "eu-central-1",
		})
		require.NoError(t, err)

		hdrs, err := handler.Do(t.Context(), map[string]string{
			":method": "POST", ":path": "/model/anthropic.claude-v2/converse",
		}, []byte(`{"inputText": "Hello from default chain"}`))
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers, "X-Amz-Date")
		require.Contains(t, headers, "Authorization")
		require.Contains(t, headers, "X-Amz-Security-Token")
		require.Equal(t, "temporary-session-token-xyz", headers["X-Amz-Security-Token"])
	})

	t.Run("different HTTP methods", func(t *testing.T) {
		awsFileBody := "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			CredentialFileLiteral: awsFileBody,
			Region:                "us-east-1",
		})
		require.NoError(t, err)

		methods := []string{"POST", "GET", "PUT"}
		for _, method := range methods {
			hdrs, err := handler.Do(t.Context(), map[string]string{
				":method": method, ":path": "/model/test-model/invoke",
			}, []byte(`{"test": "data"}`))
			require.NoError(t, err)

			headers := stringPairsToMap(hdrs)
			require.Contains(t, headers, "Authorization", "Missing Authorization for method: %s", method)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		awsFileBody := "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
		handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
			CredentialFileLiteral: awsFileBody,
			Region:                "ap-northeast-1",
		})
		require.NoError(t, err)

		hdrs, err := handler.Do(t.Context(), map[string]string{
			":method": "GET", ":path": "/model/test-model/invoke",
		}, nil)
		require.NoError(t, err)

		headers := stringPairsToMap(hdrs)
		require.Contains(t, headers, "Authorization")
		require.Contains(t, headers, "X-Amz-Date")
	})

	t.Run("multiple regions", func(t *testing.T) {
		awsFileBody := "[default]\naws_access_key_id=test\naws_secret_access_key=secret\n"
		regions := []string{"us-east-1", "eu-west-1", "ap-southeast-1"}

		for _, region := range regions {
			handler, err := newAWSHandler(t.Context(), &filterapi.AWSAuth{
				CredentialFileLiteral: awsFileBody,
				Region:                region,
			})
			require.NoError(t, err)

			_, err = handler.Do(t.Context(), map[string]string{
				":method": "POST", ":path": "/model/test/converse",
			}, nil)
			require.NoError(t, err, "Failed for region: %s", region)
		}
	})
}
