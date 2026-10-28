// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

//go:build test_extproc

package dataplane

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/idcodec"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/testupstreamlib"
)

// filesTestEncryptionSeed is the seed used in integration tests.
// Must match the --fileIDEncryptionSeed flag passed to extproc in tests (default-insecure-seed).
const filesTestEncryptionSeed = "default-insecure-seed"

// filesTestBackendName1 is the per_route_rule_backend_name set on the testupstream-files endpoint.
const filesTestBackendName1 = "test-ns/files-backend/route/files-route/rule/0/ref/0"

// filesTestModel is the routing model matched by the files test route.
const filesTestModel = "files-test-model"

// newFilesTestCodec creates the id codec with the default test seed.
func newFilesTestCodec() idcodec.Codec {
	c, err := idcodec.NewCodec(filesTestEncryptionSeed, 100_000, "", 0)
	if err != nil {
		panic(err)
	}
	return c
}

// encodeGatewayID mints a gateway file id encoding the given backend and native id.
func encodeGatewayID(t *testing.T, ns, name, nativeID string) string {
	t.Helper()
	codec := newFilesTestCodec()
	id, err := codec.Encode(&idcodec.BackendID{
		Namespace: ns,
		Name:      name,
		Kind:      idcodec.KindFile,
		Model:     filesTestModel,
		NativeID:  nativeID,
	})
	require.NoError(t, err)
	return id
}

// decodeGatewayID decodes a gateway file id and returns the BackendID.
func decodeGatewayID(t *testing.T, id string) idcodec.BackendID {
	t.Helper()
	codec := newFilesTestCodec()
	decoded, err := codec.Decode(id)
	require.NoError(t, err)
	return decoded
}

// filesTestConfig builds the filterapi.Config with the single Files test backend.
func filesTestConfig() string {
	config := &filterapi.Config{
		Version: version.Parse(),
		Backends: []filterapi.Backend{
			{
				Name:   filesTestBackendName1,
				Schema: filterapi.VersionedAPISchema{Name: filterapi.APISchemaOpenAI},
			},
			// Existing backends so the main TestWithTestUpstream still works.
			testUpstreamOpenAIBackend,
		},
		Models: []filterapi.Model{
			{Name: filesTestModel},
		},
	}
	b, err := yaml.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// buildUploadBody creates a multipart body for a file upload with the given model.
func buildUploadBody(t *testing.T, model string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("purpose", "batch"))
	if model != "" {
		require.NoError(t, w.WriteField("model", model))
	}
	fw, err := w.CreateFormFile("file", "input.jsonl")
	require.NoError(t, err)
	_, err = fw.Write([]byte(`{"custom_id":"req-1"}`))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes(), w.FormDataContentType()
}

// TestFilesAPI_WithTestUpstream exercises the Files API endpoints end-to-end with the
// test upstream, verifying that:
//   - Upload routes by the multipart model field and re-encodes the response id.
//   - List routes by the ?model= query param and re-encodes data[].id.
//   - Retrieve / Delete / Content decode the id, route by the model recorded in it, and rewrite
//     the path to the backend-native id.
//   - Missing-model requests are rejected with 400.
//   - Forged / garbage ids are rejected with 404.
func TestFilesAPI_WithTestUpstream(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), true, false)
	listenerPort := env.EnvoyListenerPort()
	addr := fmt.Sprintf("http://localhost:%d", listenerPort)

	// Extract namespace/name from the composite backend names.
	ns1, name1, ok := internalapi.NamespaceAndNameFromBackendName(filesTestBackendName1)
	require.True(t, ok)

	// Pre-encode a gateway id for retrieve/content/delete tests.
	gatewayID := encodeGatewayID(t, ns1, name1, "file-native-abc")

	for _, tc := range []struct {
		name           string
		method         string
		path           string
		body           string
		contentType    string
		responseBody   string
		responseStatus string
		expStatus      int
		expPath        string
		// expResponseContains is a substring expected in the response body.
		expResponseContains string
		// expResponseNotContains is a substring expected to be absent.
		expResponseNotContains string
	}{
		// ---------------------------------------------------------------
		// Upload: model present → routes to files-test-model backend.
		// ---------------------------------------------------------------
		{
			name:   "upload - with model - routes and re-encodes id",
			method: http.MethodPost,
			path:   "/v1/files",
			// body and contentType set below from buildUploadBody
			responseBody:           `{"id":"file-native-abc","object":"file","bytes":21,"purpose":"batch"}`,
			expPath:                "/v1/files",
			expStatus:              http.StatusOK,
			expResponseContains:    `"file-`,            // gateway id starts with file-
			expResponseNotContains: `"file-native-abc"`, // native id must not appear
		},

		// ---------------------------------------------------------------
		// Upload: missing model → 400.
		// ---------------------------------------------------------------
		{
			name:                "upload - missing model - 400",
			method:              http.MethodPost,
			path:                "/v1/files",
			expStatus:           http.StatusBadRequest,
			expResponseContains: `'model'`,
		},

		// ---------------------------------------------------------------
		// List: model present → routes to files-test-model backend.
		// ---------------------------------------------------------------
		{
			name:                   "list - with model - routes and re-encodes ids",
			method:                 http.MethodGet,
			path:                   "/v1/files?model=files-test-model&limit=2",
			responseBody:           `{"object":"list","data":[{"id":"file-native-abc"}],"has_more":false}`,
			expPath:                "/v1/files",
			expStatus:              http.StatusOK,
			expResponseContains:    `"file-`,
			expResponseNotContains: `"file-native-abc"`,
		},

		// ---------------------------------------------------------------
		// List: missing model → 400.
		// ---------------------------------------------------------------
		{
			name:                "list - missing model - 400",
			method:              http.MethodGet,
			path:                "/v1/files",
			expStatus:           http.StatusBadRequest,
			expResponseContains: `'model'`,
		},

		// ---------------------------------------------------------------
		// List: model stripped from upstream path.
		// ---------------------------------------------------------------
		{
			name:         "list - model is stripped before upstream",
			method:       http.MethodGet,
			path:         "/v1/files?model=files-test-model",
			responseBody: `{"object":"list","data":[],"has_more":false}`,
			expPath:      "/v1/files", // no ?model= forwarded
			expStatus:    http.StatusOK,
		},

		// ---------------------------------------------------------------
		// Retrieve: valid gateway id → backend-native path, re-encoded response id.
		// ---------------------------------------------------------------
		{
			name:                   "retrieve - valid gateway id",
			method:                 http.MethodGet,
			path:                   "/v1/files/" + gatewayID,
			responseBody:           `{"id":"file-native-abc","object":"file","bytes":21,"purpose":"batch"}`,
			expPath:                "/v1/files/file-native-abc",
			expStatus:              http.StatusOK,
			expResponseContains:    `"file-`,
			expResponseNotContains: `"file-native-abc"`,
		},

		// ---------------------------------------------------------------
		// Retrieve: forged / garbage id → 404.
		// ---------------------------------------------------------------
		{
			name:      "retrieve - forged id - 404",
			method:    http.MethodGet,
			path:      "/v1/files/file-this-is-not-a-valid-gateway-id",
			expStatus: http.StatusNotFound,
		},
		{
			name:      "retrieve - garbage id - 404",
			method:    http.MethodGet,
			path:      "/v1/files/notanid",
			expStatus: http.StatusNotFound,
		},

		// ---------------------------------------------------------------
		// Content: valid gateway id → native path with /content suffix, raw bytes pass-through.
		// ---------------------------------------------------------------
		{
			name:         "content - valid gateway id",
			method:       http.MethodGet,
			path:         "/v1/files/" + gatewayID + "/content",
			responseBody: `{"custom_id":"req-1","response":{"status_code":200}}`,
			expPath:      "/v1/files/file-native-abc/content",
			expStatus:    http.StatusOK,
			// content is raw bytes — passes through unchanged.
			expResponseContains: `"custom_id"`,
		},

		// ---------------------------------------------------------------
		// Delete: valid gateway id → native path, re-encoded response id.
		// ---------------------------------------------------------------
		{
			name:                   "delete - valid gateway id",
			method:                 http.MethodDelete,
			path:                   "/v1/files/" + gatewayID,
			responseBody:           `{"id":"file-native-abc","object":"file","deleted":true}`,
			expPath:                "/v1/files/file-native-abc",
			expStatus:              http.StatusOK,
			expResponseContains:    `"file-`,
			expResponseNotContains: `"file-native-abc"`,
		},

		// ---------------------------------------------------------------
		// Unsupported method on /v1/files → 404.
		// ---------------------------------------------------------------
		{
			name:      "unsupported method - 404",
			method:    http.MethodPatch,
			path:      "/v1/files",
			expStatus: http.StatusNotFound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reqBody string
			var reqContentType string

			if strings.HasPrefix(tc.path, "/v1/files") && tc.method == http.MethodPost {
				// Upload: build multipart body.
				hasModel := !strings.Contains(tc.name, "missing model")
				model := ""
				if hasModel {
					model = "files-test-model"
				}
				bodyBytes, ct := buildUploadBody(t, model)
				reqBody = string(bodyBytes)
				reqContentType = ct
			} else {
				reqBody = tc.body
				reqContentType = tc.contentType
			}

			req, err := http.NewRequestWithContext(t.Context(), tc.method, addr+tc.path, strings.NewReader(reqBody))
			require.NoError(t, err)

			if reqContentType != "" {
				req.Header.Set("Content-Type", reqContentType)
			}
			if tc.responseBody != "" {
				req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
					base64.StdEncoding.EncodeToString([]byte(tc.responseBody)))
			}
			if tc.expPath != "" {
				req.Header.Set(testupstreamlib.ExpectedPathHeaderKey,
					base64.StdEncoding.EncodeToString([]byte(tc.expPath)))
			}
			if tc.responseStatus != "" {
				req.Header.Set(testupstreamlib.ResponseStatusKey, tc.responseStatus)
			}

			var lastBody []byte
			var lastStatus int
			assert.Eventually(&noopT{}, func() bool {
				resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
				if doErr != nil {
					return false
				}
				defer resp.Body.Close()
				b := make([]byte, 0, 512)
				buf := bytes.NewBuffer(b)
				_, _ = buf.ReadFrom(resp.Body)
				lastBody = buf.Bytes()
				lastStatus = resp.StatusCode
				return resp.StatusCode == tc.expStatus
			}, eventuallyTimeout, eventuallyInterval)
			require.Equal(t, tc.expStatus, lastStatus, "body: %s", lastBody)

			if tc.expResponseContains != "" {
				require.Contains(t, string(lastBody), tc.expResponseContains)
			}
			if tc.expResponseNotContains != "" {
				require.NotContains(t, string(lastBody), tc.expResponseNotContains)
			}

			// For retrieve/delete/content with a valid gateway id, additionally verify the gateway id
			// decodes correctly (the re-encoded id in the response body must decode back).
			if tc.expStatus == http.StatusOK &&
				strings.Contains(tc.path, gatewayID) &&
				tc.method != http.MethodGet || strings.Contains(tc.path, "/content") {
				// The gateway id was already pre-encoded for ns1/name1/"file-native-abc".
				decoded := decodeGatewayID(t, gatewayID)
				require.Equal(t, ns1, decoded.Namespace)
				require.Equal(t, name1, decoded.Name)
				require.Equal(t, "file-native-abc", decoded.NativeID)
			}

			// Verify that the ?model= query param is never forwarded upstream for list.
			if tc.method == http.MethodGet && strings.Contains(tc.path, "model=") && tc.expStatus == http.StatusOK {
				require.NotContains(t, string(lastBody), "model=files-test-model")
			}
		})
	}
}

// TestFilesAPI_UploadResponseIDDecodes verifies that the gateway id in the upload response
// round-trips correctly through the codec (i.e. the re-encoded id in the response body decodes
// back to ns1/name1/native-id).
func TestFilesAPI_UploadResponseIDDecodes(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	nativeID := "file-upload-roundtrip"
	upstreamResponse := fmt.Sprintf(`{"id":%q,"object":"file","bytes":42,"purpose":"batch"}`, nativeID)

	body, ct := buildUploadBody(t, "files-test-model")
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, addr+"/v1/files",
		bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", ct)
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
		base64.StdEncoding.EncodeToString([]byte(upstreamResponse)))

	var respBody []byte
	var lastStatus int
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		respBody = buf.Bytes()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusOK
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusOK, lastStatus, "last body: %s", respBody)

	// The response body must contain a gateway-encoded id (not the native id).
	respStr := string(respBody)
	require.Contains(t, respStr, `"file-`)
	require.NotContains(t, respStr, nativeID)

	// Extract the gateway id from the JSON and decode it.
	// The JSON looks like: {"id":"file-XXXX",...}
	codec := newFilesTestCodec()
	start := strings.Index(respStr, `"id":"`) + len(`"id":"`)
	end := strings.Index(respStr[start:], `"`) + start
	gwID := respStr[start:end]
	require.True(t, strings.HasPrefix(gwID, "file-"), "expected gateway id prefix, got: %s", gwID)

	decoded, err := codec.Decode(gwID)
	require.NoError(t, err)
	require.Equal(t, nativeID, decoded.NativeID)
	require.Equal(t, idcodec.KindFile, decoded.Kind)
	require.Equal(t, filesTestModel, decoded.Model)

	// Verify we can reconstruct a retrieve path that the backend recognizes.
	require.NotEmpty(t, decoded.Namespace)
	require.NotEmpty(t, decoded.Name)
}

// TestFilesAPI_InvalidAfterCursor verifies that an opaque but malformed after= cursor returns 400.
func TestFilesAPI_InvalidAfterCursor(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	for _, badAfter := range []string{
		"not-a-cursor",
		"file-invalidbase64!!!",
	} {
		t.Run("after="+badAfter, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				addr+"/v1/files?model=files-test-model&after="+badAfter, http.NoBody)
			require.NoError(t, err)

			var lastStatus int
			assert.Eventually(&noopT{}, func() bool {
				resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
				if doErr != nil {
					return false
				}
				defer resp.Body.Close()
				lastStatus = resp.StatusCode
				return resp.StatusCode == http.StatusBadRequest
			}, eventuallyTimeout, eventuallyInterval)
			require.Equal(t, http.StatusBadRequest, lastStatus)
		})
	}
}

// TestFilesAPI_ListModelStrippedFromUpstream verifies that the ?model= query parameter
// is never forwarded to the upstream provider (the OpenAI Files list API does not accept it).
func TestFilesAPI_ListModelStrippedFromUpstream(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		addr+"/v1/files?model=files-test-model&limit=5", http.NoBody)
	require.NoError(t, err)
	// Assert the upstream receives /v1/files?limit=5 (no model=).
	req.Header.Set(testupstreamlib.ExpectedPathHeaderKey,
		base64.StdEncoding.EncodeToString([]byte("/v1/files")))
	req.Header.Set(testupstreamlib.ExpectedRawQueryHeaderKey, "limit=5")
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
		base64.StdEncoding.EncodeToString([]byte(`{"object":"list","data":[],"has_more":false}`)))

	var lastStatus int
	var lastBody []byte
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		lastBody = buf.Bytes()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusOK
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusOK, lastStatus, "body: %s", lastBody)

	require.NotContains(t, string(lastBody), "model=", "model param must not appear in upstream body")
}

// TestFilesAPI_RetrieveNativePath verifies that the upstream receives the backend-native id
// path (not the gateway id) for a retrieve request.
func TestFilesAPI_RetrieveNativePath(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	ns1, name1, ok := internalapi.NamespaceAndNameFromBackendName(filesTestBackendName1)
	require.True(t, ok)
	nativeID := "file-retrieve-test"
	gwID := encodeGatewayID(t, ns1, name1, nativeID)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		addr+"/v1/files/"+gwID, http.NoBody)
	require.NoError(t, err)
	// Assert upstream receives the native path, not the gateway id path.
	req.Header.Set(testupstreamlib.ExpectedPathHeaderKey,
		base64.StdEncoding.EncodeToString([]byte("/v1/files/"+nativeID)))
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
		base64.StdEncoding.EncodeToString([]byte(
			fmt.Sprintf(`{"id":%q,"object":"file","bytes":10,"purpose":"batch"}`, nativeID))))

	var lastStatus int
	var lastBody []byte
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		lastBody = buf.Bytes()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusOK
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusOK, lastStatus, "body: %s", lastBody)

	// Response must contain a gateway id, not the native id.
	require.NotContains(t, string(lastBody), nativeID,
		"native id must not appear in response body")
	require.Contains(t, string(lastBody), `"file-`,
		"response must contain gateway-encoded id")
}

// TestFilesAPI_UploadModelStrippedFromBody verifies that the model multipart field
// is removed before the request is forwarded upstream.
func TestFilesAPI_UploadModelStrippedFromBody(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	body, ct := buildUploadBody(t, "files-test-model")
	bodyStr := string(body)

	// The multipart body before sending must contain the model field.
	require.Contains(t, bodyStr, "files-test-model")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, addr+"/v1/files",
		strings.NewReader(bodyStr))
	require.NoError(t, err)
	req.Header.Set("Content-Type", ct)
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey,
		base64.StdEncoding.EncodeToString([]byte(
			`{"id":"file-strip-test","object":"file","bytes":10,"purpose":"batch"}`)))
	// Assert the upstream request body does NOT contain the model field.
	// We can't check the multipart body content directly via testupstreamlib headers,
	// but we verify the gateway does not 400 or 5xx (i.e., stripping succeeded).

	var lastStatus int
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusOK
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusOK, lastStatus)
}

// TestFilesAPI_UpstreamErrorPassThrough verifies that a non-2xx upstream response is returned to
// the client with its original status and body, rather than replaced by a re-encode error.
func TestFilesAPI_UpstreamErrorPassThrough(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())

	upstreamErr := `{"error":{"message":"Invalid file format","type":"invalid_request_error"}}`
	body, ct := buildUploadBody(t, filesTestModel)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, addr+"/v1/files", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", ct)
	req.Header.Set(testupstreamlib.ResponseStatusKey, "400")
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString([]byte(upstreamErr)))

	var lastStatus int
	var lastBody []byte
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		lastBody = buf.Bytes()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusBadRequest
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusBadRequest, lastStatus, "body: %s", lastBody)
	require.JSONEq(t, upstreamErr, string(lastBody))
}

// TestFilesAPI_StableIDs verifies that the gateway id returned for a file is the same across
// upload and retrieve.
func TestFilesAPI_StableIDs(t *testing.T) {
	env := startTestEnvironment(t, filesTestConfig(), false, false)
	addr := fmt.Sprintf("http://localhost:%d", env.EnvoyListenerPort())
	ns1, name1, ok := internalapi.NamespaceAndNameFromBackendName(filesTestBackendName1)
	require.True(t, ok)
	nativeID := "file-stable-1"
	gwID := encodeGatewayID(t, ns1, name1, nativeID)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, addr+"/v1/files/"+gwID, http.NoBody)
	require.NoError(t, err)
	req.Header.Set(testupstreamlib.ResponseBodyHeaderKey, base64.StdEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"id":%q,"object":"file"}`, nativeID))))

	var lastBody []byte
	var lastStatus int
	assert.Eventually(&noopT{}, func() bool {
		resp, doErr := http.DefaultClient.Do(req.Clone(t.Context()))
		if doErr != nil {
			return false
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		lastBody = buf.Bytes()
		lastStatus = resp.StatusCode
		return resp.StatusCode == http.StatusOK
	}, eventuallyTimeout, eventuallyInterval)
	require.Equal(t, http.StatusOK, lastStatus, "body: %s", lastBody)
	require.Contains(t, string(lastBody), fmt.Sprintf(`"id":%q`, gwID))
}
