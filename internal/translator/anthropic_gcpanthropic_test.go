// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
)

func TestAnthropicToGCPAnthropicTranslator_RequestBody_ModelNameOverride(t *testing.T) {
	tests := []struct {
		name           string
		override       string
		inputModel     string
		expectedModel  string
		expectedInPath string
	}{
		{
			name:           "no override uses original model",
			override:       "",
			inputModel:     "claude-3-haiku-20240307",
			expectedModel:  "claude-3-haiku-20240307",
			expectedInPath: "claude-3-haiku-20240307",
		},
		{
			name:           "override replaces model in body and path",
			override:       "claude-3-sonnet-override",
			inputModel:     "claude-3-haiku-20240307",
			expectedModel:  "claude-3-sonnet-override",
			expectedInPath: "claude-3-sonnet-override",
		},
		{
			name:           "override with empty input model",
			override:       "claude-3-opus-20240229",
			inputModel:     "",
			expectedModel:  "claude-3-opus-20240229",
			expectedInPath: "claude-3-opus-20240229",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", tt.override)

			// Create the request using map structure.
			originalReq := &anthropic.MessagesRequest{
				Model: tt.inputModel,
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageRoleUser,
						Content: anthropic.MessageContent{
							Text: "Hello",
						},
					},
				},
			}

			headerMutation, bodyMutation, err := translator.RequestBody(nil, originalReq, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)
			require.NotNil(t, bodyMutation)

			// Check path header contains expected model.
			pathHeader := headerMutation[0]
			require.Equal(t, gcpPathHeaderName, pathHeader.Key())
			expectedPath := "publishers/anthropic/models/" + tt.expectedInPath + ":rawPredict"
			assert.Equal(t, expectedPath, pathHeader.Value())

			// Check that model field is removed from body (since it's in the path).
			var modifiedReq map[string]any
			err = json.Unmarshal(bodyMutation, &modifiedReq)
			require.NoError(t, err)
			_, hasModel := modifiedReq["model"]
			assert.False(t, hasModel, "model field should be removed from request body")
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_ComprehensiveMarshalling(t *testing.T) {
	translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")

	// Create a comprehensive MessagesRequest with all possible fields using map structure.
	originalReq := &anthropic.MessagesRequest{
		Model: "claude-3-opus-20240229",
		Messages: []anthropic.MessageParam{
			{
				Role:    anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{Text: "Hello, how are you?"},
			},
			{
				Role: anthropic.MessageRoleAssistant,
				Content: anthropic.MessageContent{
					Array: []anthropic.ContentBlockParam{
						{Text: &anthropic.TextBlockParam{Text: "I'm doing well, thank you!"}},
					},
				},
			},
			{
				Role: anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{
					Array: []anthropic.ContentBlockParam{
						{Text: &anthropic.TextBlockParam{Text: "Can you help me with the weather?"}},
					},
				},
			},
		},
		MaxTokens:     1024,
		Stream:        false,
		Temperature:   func() *float64 { v := 0.7; return &v }(),
		TopP:          func() *float64 { v := 0.95; return &v }(),
		StopSequences: []string{"Human:", "Assistant:"},
		System:        &anthropic.SystemPrompt{Text: "You are a helpful weather assistant."},
		Tools: []anthropic.ToolUnion{
			{Tool: &anthropic.Tool{
				Name:        "get_weather",
				Description: "Get current weather information",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string","description":"City name"}},"required":["location"]}`),
			}},
		},
		ToolChoice: &anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}},
	}

	raw, err := json.Marshal(originalReq)
	require.NoError(t, err)

	headerMutation, bodyMutation, err := translator.RequestBody(raw, originalReq, false)
	require.NoError(t, err)
	require.NotNil(t, headerMutation)
	require.NotNil(t, bodyMutation)

	var outputReq map[string]any
	err = json.Unmarshal(bodyMutation, &outputReq)
	require.NoError(t, err)

	require.NotContains(t, outputReq, "model", "model field should be removed for GCP")

	require.Contains(t, outputReq, "anthropic_version", "should add anthropic_version for GCP")
	require.Equal(t, "2023-06-01", outputReq["anthropic_version"])

	messages, ok := outputReq["messages"].([]any)
	require.True(t, ok, "messages should be an array")
	require.Len(t, messages, 3, "should have 3 messages")

	require.Equal(t, float64(1024), outputReq["max_tokens"])
	// stream: false is now included in the map
	require.NotContains(t, outputReq, "stream")
	require.Equal(t, 0.7, outputReq["temperature"])
	require.Equal(t, 0.95, outputReq["top_p"])
	require.Equal(t, "You are a helpful weather assistant.", outputReq["system"])

	stopSeq, ok := outputReq["stop_sequences"].([]any)
	require.True(t, ok, "stop_sequences should be an array")
	require.Len(t, stopSeq, 2)
	require.Equal(t, "Human:", stopSeq[0])
	require.Equal(t, "Assistant:", stopSeq[1])

	tools, ok := outputReq["tools"].([]any)
	require.True(t, ok, "tools should be an array")
	require.Len(t, tools, 1)

	toolChoice, ok := outputReq["tool_choice"].(map[string]any)
	require.True(t, ok, "tool_choice should be an object")

	require.NotEmpty(t, toolChoice)

	pathHeader := headerMutation[0]
	require.Equal(t, gcpPathHeaderName, pathHeader.Key())
	expectedPath := "publishers/anthropic/models/claude-3-opus-20240229:rawPredict"
	require.Equal(t, expectedPath, pathHeader.Value())
}

func TestAnthropicToGCPAnthropicTranslator_BackendVersionHandling(t *testing.T) {
	tests := []struct {
		name            string
		backendVersion  string
		expectedVersion string
		shouldError     bool
	}{
		{
			name:            "no version configured should error",
			backendVersion:  "",
			expectedVersion: "",
			shouldError:     true,
		},
		{
			name:            "backend version only",
			backendVersion:  "2023-06-01",
			expectedVersion: "2023-06-01",
			shouldError:     false,
		},
		{
			name:            "custom backend version",
			backendVersion:  "2024-01-01",
			expectedVersion: "2024-01-01",
			shouldError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToGCPAnthropicTranslator(tt.backendVersion, "")

			originalReq := &anthropic.MessagesRequest{
				Model: "claude-3-sonnet-20240229",
				Messages: []anthropic.MessageParam{
					{
						Role: anthropic.MessageRoleUser,
						Content: anthropic.MessageContent{
							Array: []anthropic.ContentBlockParam{
								{Text: &anthropic.TextBlockParam{Text: "Hello"}},
							},
						},
					},
				},
				MaxTokens: 100,
			}

			_, bodyMutation, err := translator.RequestBody(nil, originalReq, false)

			if tt.shouldError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "anthropic_version is required for GCP Vertex AI")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, bodyMutation)

			var outputReq map[string]any
			err = json.Unmarshal(bodyMutation, &outputReq)
			require.NoError(t, err)

			require.Contains(t, outputReq, "anthropic_version")
			require.Equal(t, tt.expectedVersion, outputReq["anthropic_version"])
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_RequestBody_StreamingPaths(t *testing.T) {
	tests := []struct {
		name              string
		stream            any
		expectedSpecifier string
	}{
		{
			name:              "non-streaming uses rawPredict",
			stream:            false,
			expectedSpecifier: "rawPredict",
		},
		{
			name:              "streaming uses streamRawPredict",
			stream:            true,
			expectedSpecifier: "streamRawPredict",
		},
		{
			name:              "missing stream defaults to rawPredict",
			stream:            nil,
			expectedSpecifier: "rawPredict",
		},
		{
			name:              "non-boolean stream defaults to rawPredict",
			stream:            "true",
			expectedSpecifier: "rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")

			parsedReq := &anthropic.MessagesRequest{
				Model: "claude-3-sonnet-20240229",
				Messages: []anthropic.MessageParam{
					{
						Role:    anthropic.MessageRoleUser,
						Content: anthropic.MessageContent{Text: "Test"},
					},
				},
			}
			if tt.stream != nil {
				if streamVal, ok := tt.stream.(bool); ok {
					parsedReq.Stream = streamVal
				}
			}

			headerMutation, _, err := translator.RequestBody(nil, parsedReq, false)
			require.NoError(t, err)
			require.NotNil(t, headerMutation)

			// Check path contains expected specifier.
			pathHeader := headerMutation[0]
			expectedPath := "publishers/anthropic/models/claude-3-sonnet-20240229:" + tt.expectedSpecifier
			assert.Equal(t, gcpPathHeaderName, pathHeader.Key())
			assert.Equal(t, expectedPath, pathHeader.Value())
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_RequestBody_FieldPassthrough(t *testing.T) {
	translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")

	temp := 0.7
	topP := 0.95
	topK := 40
	parsedReq := &anthropic.MessagesRequest{
		Model: "claude-3-sonnet-20240229",
		Messages: []anthropic.MessageParam{
			{
				Role:    anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{Text: "Hello, world!"},
			},
			{
				Role:    anthropic.MessageRoleAssistant,
				Content: anthropic.MessageContent{Text: "Hi there!"},
			},
			{
				Role:    anthropic.MessageRoleUser,
				Content: anthropic.MessageContent{Text: "How are you?"},
			},
		},
		MaxTokens:     1000,
		Temperature:   &temp,
		TopP:          &topP,
		TopK:          &topK,
		StopSequences: []string{"Human:", "Assistant:"},
		Stream:        false,
		System:        &anthropic.SystemPrompt{Text: "You are a helpful assistant"},
		Tools: []anthropic.ToolUnion{
			{Tool: &anthropic.Tool{
				Name:        "get_weather",
				Description: "Get weather info",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}}}`),
			}},
		},
		ToolChoice: &anthropic.ToolChoice{Auto: &anthropic.ToolChoiceAuto{Type: "auto"}},
		Metadata:   &anthropic.MessagesMetadata{UserID: ptr.To("test123")},
	}

	raw, err := json.Marshal(parsedReq)
	require.NoError(t, err)

	_, bodyMutation, err := translator.RequestBody(raw, parsedReq, false)
	require.NoError(t, err)
	require.NotNil(t, bodyMutation)

	var modifiedReq map[string]any
	err = json.Unmarshal(bodyMutation, &modifiedReq)
	require.NoError(t, err)

	// Messages should be preserved.
	require.Len(t, modifiedReq["messages"], 3)

	// Numeric fields get converted to float64 by JSON unmarshalling.
	require.Equal(t, float64(1000), modifiedReq["max_tokens"])
	require.Equal(t, 0.7, modifiedReq["temperature"])
	require.Equal(t, 0.95, modifiedReq["top_p"])
	require.Equal(t, float64(40), modifiedReq["top_k"])

	// Arrays become []interface{} by JSON unmarshalling.
	stopSeq, ok := modifiedReq["stop_sequences"].([]any)
	require.True(t, ok)
	require.Len(t, stopSeq, 2)
	require.Equal(t, "Human:", stopSeq[0])
	require.Equal(t, "Assistant:", stopSeq[1])

	// Boolean false values are now included in the map.
	require.NotContains(t, modifiedReq, "stream")

	// String values are preserved.
	require.Equal(t, "You are a helpful assistant", modifiedReq["system"])

	// Complex objects should be preserved as maps.
	require.NotNil(t, modifiedReq["tools"])
	require.NotNil(t, modifiedReq["tool_choice"])
	require.NotNil(t, modifiedReq["metadata"])

	// Verify model field is removed from body (it's in the path instead).
	_, hasModel := modifiedReq["model"]
	require.False(t, hasModel, "model field should be removed from request body")

	// Verify anthropic_version is added from the backend configuration.
	require.Equal(t, "2023-06-01", modifiedReq["anthropic_version"])
}

func TestAnthropicToGCPAnthropicTranslator_HeaderValueFilter(t *testing.T) {
	parsedReq := &anthropic.MessagesRequest{
		Model: "claude-3-sonnet-20240229",
		Messages: []anthropic.MessageParam{
			{Role: anthropic.MessageRoleUser, Content: anthropic.MessageContent{Text: "Hello"}},
		},
	}

	tests := []struct {
		name           string
		requestHeaders map[string]string
		filterHeader   string
		filterMode     string
		filterValues   []string
		wantBetaHeader string
		wantOverwrite  bool
	}{
		{
			name:           "no anthropic-beta header sent",
			requestHeaders: map[string]string{},
			filterHeader:   "anthropic-beta",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantOverwrite:  false,
		},
		{
			name:           "no filter configured leaves header untouched",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,thinking-token-count-2026-05-13"},
			wantOverwrite:  false,
		},
		{
			name:           "denylist drops the unsupported value",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,thinking-token-count-2026-05-13"},
			filterHeader:   "anthropic-beta",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantBetaHeader: "advanced-tool-use-2025-11-20",
			wantOverwrite:  true,
		},
		{
			name:           "denylist with no matching value leaves header untouched",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20"},
			filterHeader:   "anthropic-beta",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantOverwrite:  false,
		},
		{
			name:           "allowlist keeps only the sanctioned value",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,thinking-token-count-2026-05-13"},
			filterHeader:   "anthropic-beta",
			filterMode:     "Allowlist",
			filterValues:   []string{"advanced-tool-use-2025-11-20"},
			wantBetaHeader: "advanced-tool-use-2025-11-20",
			wantOverwrite:  true,
		},
		{
			// The setter is called for every configured filter, so it must ignore headers it does
			// not forward itself rather than applying someone else's value list to anthropic-beta.
			name:           "filter on a different header is ignored",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,thinking-token-count-2026-05-13"},
			filterHeader:   "x-some-other-header",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantOverwrite:  false,
		},
		{
			// Header names are case-insensitive, so a filter configured as Anthropic-Beta must still
			// reach the anthropic-beta handling here.
			name:           "filter header name matching is case-insensitive",
			requestHeaders: map[string]string{"anthropic-beta": "advanced-tool-use-2025-11-20,thinking-token-count-2026-05-13"},
			filterHeader:   "Anthropic-Beta",
			filterMode:     "Denylist",
			filterValues:   []string{"thinking-token-count-2026-05-13"},
			wantBetaHeader: "advanced-tool-use-2025-11-20",
			wantOverwrite:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")
			tr.(RequestHeadersSetter).SetRequestHeaders(tt.requestHeaders)
			if tt.filterHeader != "" {
				tr.(HeaderValueFilterSetter).SetHeaderValueFilter(tt.filterHeader, tt.filterMode, tt.filterValues)
			}

			headerMutation, _, err := tr.RequestBody(nil, parsedReq, false)
			require.NoError(t, err)

			var betaHeader *internalapi.Header
			for i := range headerMutation {
				if headerMutation[i].Key() == anthropicBetaHeaderName {
					betaHeader = &headerMutation[i]
				}
			}
			if tt.wantOverwrite {
				require.NotNil(t, betaHeader, "expected anthropic-beta header to be overwritten")
				assert.Equal(t, tt.wantBetaHeader, betaHeader.Value())
			} else {
				require.Nil(t, betaHeader, "anthropic-beta header should not be overwritten")
			}
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_ResponseHeaders(t *testing.T) {
	translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name:    "empty headers",
			headers: map[string]string{},
		},
		{
			name: "various headers",
			headers: map[string]string{
				"content-type":  "application/json",
				"authorization": "Bearer token",
				"custom-header": "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headerMutation, err := translator.ResponseHeaders(tt.headers)
			require.NoError(t, err)
			assert.Nil(t, headerMutation, "ResponseHeaders should return nil for passthrough")
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_ResponseBody_ZeroTokenUsage(t *testing.T) {
	translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")

	// Test response with zero token usage.
	respBody := anthropic.MessagesResponse{
		ID:      "msg_zero",
		Type:    "message",
		Role:    "assistant",
		Content: []anthropic.MessagesContentBlock{{Text: &anthropic.TextBlock{Text: "Hello"}}},
		Model:   "claude-3-sonnet-20240229",
		Usage: &anthropic.Usage{
			InputTokens:  0,
			OutputTokens: 0,
		},
	}

	bodyBytes, err := json.Marshal(respBody)
	require.NoError(t, err)

	bodyReader := bytes.NewReader(bodyBytes)
	respHeaders := map[string]string{"content-type": "application/json"}

	_, _, tokenUsage, _, err := translator.ResponseBody(respHeaders, bodyReader, true, nil)
	require.NoError(t, err)

	expected := tokenUsageFrom(0, 0, 0, 0, 0, -1)
	assert.Equal(t, expected, tokenUsage)
}

func TestAnthropicToGCPAnthropicTranslator_ResponseBody_StreamingTokenUsage(t *testing.T) {
	tests := []struct {
		name          string
		chunk         string
		endOfStream   bool
		expectedUsage metrics.TokenUsage
	}{
		{
			name:          "regular streaming chunk without usage",
			chunk:         "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" to me.\"}}\n\n",
			endOfStream:   false,
			expectedUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
		{
			name:          "message_delta chunk with token usage",
			chunk:         "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":84}}\n\n",
			endOfStream:   false,
			expectedUsage: tokenUsageFrom(0, 0, 0, 84, 84, -1),
		},
		{
			name:          "message_stop chunk without usage",
			chunk:         "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			endOfStream:   false,
			expectedUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
		{
			name:          "invalid json chunk",
			chunk:         "event: invalid\ndata: {\"invalid\": \"json\"}\n\n",
			endOfStream:   false,
			expectedUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
		{
			name:          "message_delta with decimal output_tokens",
			chunk:         "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":42.0}}\n\n",
			endOfStream:   false,
			expectedUsage: tokenUsageFrom(0, 0, 0, 42, 42, -1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyReader := bytes.NewReader([]byte(tt.chunk))
			respHeaders := map[string]string{"content-type": "application/json"}
			translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")
			translator.(*anthropicToGCPAnthropicTranslator).stream = true
			headerMutation, bodyMutation, tokenUsage, _, err := translator.ResponseBody(respHeaders, bodyReader, tt.endOfStream, nil)

			require.NoError(t, err)
			require.Nil(t, headerMutation)
			require.Nil(t, bodyMutation)
			require.Equal(t, tt.expectedUsage, tokenUsage)
		})
	}
}

func TestAnthropicToGCPAnthropicTranslator_ResponseBody_StreamingEdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		chunk         string
		expectedUsage metrics.TokenUsage
	}{
		{
			name:          "message_start without message field",
			chunk:         "event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
			expectedUsage: metrics.TokenUsage{},
		},
		{
			name:          "message_start without usage field",
			chunk:         "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\"}}\n\n",
			expectedUsage: metrics.TokenUsage{},
		},
		{
			name:          "message_delta without usage field",
			chunk:         "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
			expectedUsage: tokenUsageFrom(0, 0, 0, 0, 0, -1),
		},
		{
			name:          "invalid json in data",
			chunk:         "event: message_start\ndata: {invalid json}\n\n",
			expectedUsage: tokenUsageFrom(-1, -1, -1, -1, -1, -1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodyReader := bytes.NewReader([]byte(tt.chunk))
			respHeaders := map[string]string{"content-type": "application/json"}
			translator := NewAnthropicToGCPAnthropicTranslator("2023-06-01", "")
			translator.(*anthropicToGCPAnthropicTranslator).stream = true
			headerMutation, bodyMutation, tokenUsage, _, err := translator.ResponseBody(respHeaders, bodyReader, false, nil)

			require.NoError(t, err)
			require.Nil(t, headerMutation)
			require.Nil(t, bodyMutation)
			require.Equal(t, tt.expectedUsage, tokenUsage)
		})
	}
}

func tokenUsageFrom(in, cachedInput, cacheCreationInput, out, total, reasoning int32) metrics.TokenUsage {
	var usage metrics.TokenUsage
	if in >= 0 {
		usage.SetInputTokens(uint32(in))
	}
	if cachedInput >= 0 {
		usage.SetCachedInputTokens(uint32(cachedInput))
	}
	if cacheCreationInput >= 0 {
		usage.SetCacheCreationInputTokens(uint32(cacheCreationInput))
	}
	if out >= 0 {
		usage.SetOutputTokens(uint32(out))
	}
	if total >= 0 {
		usage.SetTotalTokens(uint32(total))
	}
	if reasoning >= 0 {
		usage.SetReasoningTokens(uint32(reasoning))
	}
	return usage
}

func TestAnthropicToGCPAnthropicTranslator_ResponseBody_StreamingFullScenario(t *testing.T) {
	// Test to reproduce and verify fix for the input_token=0 issue in Anthropic streaming
	// This test verifies that input_tokens from message_start are preserved when
	// message_delta doesn't provide input_tokens (real-world scenario)

	translator := NewAnthropicToGCPAnthropicTranslator("v1", "")

	// Simulate request body to set stream=true
	reqBody := anthropic.MessagesRequest{
		Stream: true,
		Model:  "claude-3-sonnet-20240229",
	}
	_, _, err := translator.RequestBody([]byte(`{"stream":true}`), &reqBody, false)
	require.NoError(t, err)

	// Sample streaming response from Anthropic with realistic flow:
	// 1. message_start provides input_tokens=15
	// 2. content_block events provide the actual text content
	// 3. message_delta at the end provides output_tokens=5 but no input_tokens
	// 4. message_stop ends the stream
	messageStartChunk := `event: message_start
data: {"type": "message_start", "message": {"id": "msg_123", "type": "message", "role": "assistant", "content": [], "model": "claude-3-sonnet-20240229", "usage": {"input_tokens": 15, "cache_read_input_tokens": 5,  "cache_creation_input_tokens": 1, "output_tokens": 0}}}
`
	contentBlockStartChunk := `event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}
`
	contentBlockDeltaChunk := `event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}
`
	contentBlockStopChunk := `event: content_block_stop
data: {"type": "content_block_stop", "index": 0}
`
	messageDeltaChunk := `event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 5}}
`
	messageStopChunk := `event: message_stop
data: {"type": "message_stop"}
`

	// Process the streaming response
	_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(messageStartChunk), false, nil)
	require.NoError(t, err)

	// Verify token usage - this should calculate input_tokens from message_start
	inputTokens, inputSet := tokenUsage.InputTokens()
	outputTokens, outputSet := tokenUsage.OutputTokens()
	totalTokens, totalSet := tokenUsage.TotalTokens()
	cachedTokens, cachedSet := tokenUsage.CachedInputTokens()
	cacheCreationTokens, cacheCreationSet := tokenUsage.CacheCreationInputTokens()

	// Assertions
	assert.True(t, inputSet, "Input tokens should be set")
	assert.Equal(t, uint32(21), inputTokens, "Input tokens should be preserved from message_start")

	assert.True(t, outputSet, "Output tokens should be set")
	assert.Equal(t, uint32(0), outputTokens, "Output tokens should come from message_delta")

	assert.True(t, totalSet, "Total tokens should be calculated")
	assert.Equal(t, uint32(21), totalTokens, "Total tokens should be input + output")

	assert.True(t, cachedSet, "Cached tokens should be set")
	assert.Equal(t, uint32(5), cachedTokens, "No cached tokens in this scenario")

	assert.True(t, cacheCreationSet, "cache creation tokens should be set")
	assert.Equal(t, uint32(1), cacheCreationTokens, "No cache creation tokens in this scenario")

	_, _, tokenUsage, _, err = translator.ResponseBody(nil, strings.NewReader(contentBlockStartChunk), false, nil)
	require.NoError(t, err)
	_, _, tokenUsage, _, err = translator.ResponseBody(nil, strings.NewReader(contentBlockDeltaChunk), false, nil)
	require.NoError(t, err)
	_, _, tokenUsage, _, err = translator.ResponseBody(nil, strings.NewReader(contentBlockStopChunk), false, nil)
	require.NoError(t, err)
	_, _, tokenUsage, _, err = translator.ResponseBody(nil, strings.NewReader(messageDeltaChunk), false, nil)
	require.NoError(t, err)
	_, _, tokenUsage, _, err = translator.ResponseBody(nil, strings.NewReader(messageStopChunk), false, nil)
	require.NoError(t, err)

	// Verify token usage - this should preserve input_tokens from message_start and get the output_tokens from message_delta
	inputTokens, inputSet = tokenUsage.InputTokens()
	outputTokens, outputSet = tokenUsage.OutputTokens()
	totalTokens, totalSet = tokenUsage.TotalTokens()
	cachedTokens, cachedSet = tokenUsage.CachedInputTokens()
	cacheCreationTokens, cacheCreationSet = tokenUsage.CacheCreationInputTokens()

	assert.True(t, inputSet, "Input tokens should be set")
	assert.Equal(t, uint32(21), inputTokens, "Input tokens should be preserved from message_start")

	assert.True(t, outputSet, "Output tokens should be set")
	assert.Equal(t, uint32(5), outputTokens, "Output tokens should come from message_delta")

	assert.True(t, totalSet, "Total tokens should be calculated")
	assert.Equal(t, uint32(26), totalTokens, "Total tokens should be input + output")

	assert.True(t, cachedSet, "Cached tokens should be set")
	assert.Equal(t, uint32(5), cachedTokens, "No cached tokens in this scenario")

	assert.True(t, cacheCreationSet, "cache creation tokens should be set")
	assert.Equal(t, uint32(1), cacheCreationTokens, "No cache creation tokens in this scenario")
}
