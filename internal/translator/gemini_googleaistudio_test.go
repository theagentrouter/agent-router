// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestGeminiGenerateContentTranslatorRequest(t *testing.T) {
	const prefix = "/gemini/v1beta/models"
	path, err := gcp.ParseGeminiGenerateContentPath(prefix, prefix+"/alias:streamGenerateContent?key=client-key&alt=sse&%24fields=candidates")
	require.NoError(t, err)
	tr := NewGeminiGenerateContentTranslator(path, "served")
	raw := []byte(`{"contents":[{"parts":[{"thoughtSignature":"opaque*signature"}]}],"cachedContent":"cachedContents/123"}`)
	req := &gcp.GeminiGenerateContentRequest{Model: "alias"}

	headers, body, err := tr.RequestBody(raw, req, false)
	require.NoError(t, err)
	require.Nil(t, body)
	require.Equal(t, []internalapi.Header{{":path", "/v1beta/models/served:streamGenerateContent?alt=sse"}}, headers)

	headers, body, err = tr.RequestBody(raw, req, true)
	require.NoError(t, err)
	require.Equal(t, raw, body)
	require.Contains(t, headers, internalapi.Header{"content-length", fmt.Sprint(len(raw))})
}

func TestGeminiGenerateContentTranslatorUsage(t *testing.T) {
	const prefix = "/gemini/v1beta/models"
	const first = `{"modelVersion":"served-001","usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4}}`
	const last = `{"candidates":[{"content":{"parts":[{"text":"quoted \" } {\"","thoughtSignature":"opaque*signature"}]}}],` +
		`"usageMetadata":{"toolUsePromptTokenCount":2,"candidatesTokenCount":5,"thoughtsTokenCount":3,"totalTokenCount":20}}`
	for _, tc := range []struct {
		name, method, contentType, content string
	}{
		{"unary", "generateContent", "application/json", `{"modelVersion":"served-001","usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":4,"toolUsePromptTokenCount":2,"candidatesTokenCount":5,"thoughtsTokenCount":3,"totalTokenCount":20}}`},
		{"SSE", "streamGenerateContent", "text/event-stream", "data: " + first + "\r\n\r\ndata: " + last + "\r\n\r\n"},
		{"mixed SSE line endings", "streamGenerateContent", "text/event-stream", "\xef\xbb\xbfdata: " + first + "\n\ndata: " + last + "\r\r\n"},
		{"SSE ending in CR", "streamGenerateContent", "text/event-stream", "data: " + first + "\r\rdata: " + last + "\r\r"},
		{"JSON array", "streamGenerateContent", "application/json", "[" + first + "," + last + "]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := gcp.ParseGeminiGenerateContentPath(prefix, prefix+"/alias:"+tc.method)
			require.NoError(t, err)
			headers := map[string]string{"content-type": tc.contentType}
			for cut := 0; cut < len(tc.content); cut++ {
				tr := NewGeminiGenerateContentTranslator(path, "served")
				var usage metrics.TokenUsage
				var model string
				chunks := []string{tc.content}
				if path.Streaming() {
					chunks = []string{tc.content[:cut], tc.content[cut:]}
				}
				for i, chunk := range chunks {
					responseHeaders, body, observedUsage, observedModel, err := tr.ResponseBody(headers, strings.NewReader(chunk), i == len(chunks)-1, nil)
					require.NoError(t, err)
					require.Nil(t, responseHeaders)
					require.Nil(t, body)
					usage, model = observedUsage, observedModel
				}
				require.Equal(t, "served-001", model)
				require.Equal(t, tokenUsageFrom(12, 4, -1, 8, 20, 3), usage)
				if !path.Streaming() {
					break
				}
			}
		})
	}
}
