// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestBatchNewParams_JSON(t *testing.T) {
	in := `{"input_file_id":"file-abc","endpoint":"/v1/chat/completions","completion_window":"24h","metadata":{"k":"v"}}`
	var p BatchNewParams
	require.NoError(t, json.Unmarshal([]byte(in), &p))
	require.Equal(t, "file-abc", p.InputFileID)
	require.Equal(t, BatchNewParamsEndpointV1ChatCompletions, p.Endpoint)
	require.Equal(t, BatchNewParamsCompletionWindow24h, p.CompletionWindow)
	require.Equal(t, map[string]string{"k": "v"}, p.Metadata)
	require.Nil(t, p.OutputExpiresAfter)

	out, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, in, string(out))
}

func TestBatch_JSONRoundTrip(t *testing.T) {
	b := Batch{
		ID:               "batch_123",
		Object:           "batch",
		Endpoint:         BatchNewParamsEndpointV1Embeddings,
		InputFileID:      "file-in",
		CompletionWindow: "24h",
		Status:           BatchStatusCompleted,
		OutputFileID:     ptr.To("file-out"),
		CreatedAt:        1711471533,
		CompletedAt:      ptr.To(int64(1711493133)),
		RequestCounts:    &BatchRequestCounts{Total: 3, Completed: 2, Failed: 1},
		Usage: &BatchUsage{
			InputTokens:         10,
			OutputTokens:        5,
			TotalTokens:         15,
			InputTokensDetails:  &BatchUsageInputTokensDetails{CachedTokens: 2},
			OutputTokensDetails: &BatchUsageOutputTokensDetails{ReasoningTokens: 1},
		},
	}
	data, err := json.Marshal(b)
	require.NoError(t, err)

	var got Batch
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, b, got)

	// Unset optional fields must be omitted.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	require.NotContains(t, raw, "error_file_id")
	require.NotContains(t, raw, "failed_at")
	require.NotContains(t, raw, "errors")
}

func TestBatchListResponse_JSON(t *testing.T) {
	in := `{"object":"list","data":[{"id":"batch_1","object":"batch","endpoint":"/v1/responses","input_file_id":"file-1","completion_window":"24h","status":"in_progress","created_at":1}],"has_more":true,"first_id":"batch_1","last_id":"batch_1"}`
	var l BatchListResponse
	require.NoError(t, json.Unmarshal([]byte(in), &l))
	require.Len(t, l.Data, 1)
	require.Equal(t, BatchStatusInProgress, l.Data[0].Status)
	require.Equal(t, BatchNewParamsEndpointV1Responses, l.Data[0].Endpoint)
	require.True(t, l.HasMore)
	require.Equal(t, "batch_1", l.LastID)
}
