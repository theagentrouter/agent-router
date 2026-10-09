// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	anthropicschema "github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestAnthropicToAnthropic_RequestBody(t *testing.T) {
	for _, tc := range []struct {
		name              string
		original          []byte
		body              anthropicschema.MessagesRequest
		forceBodyMutation bool
		modelNameOverride string
		prefix            string

		expRequestModel internalapi.RequestModel
		expNewBody      []byte
		expPath         string
	}{
		{
			name:              "no mutation",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			body:              anthropicschema.MessagesRequest{Stream: false, Model: "claude-2"},
			forceBodyMutation: false,
			modelNameOverride: "",
			prefix:            "v1",
			expRequestModel:   "claude-2",
			expNewBody:        nil,
			expPath:           "/v1/messages",
		},
		{
			name:              "model override",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}], Stream: true}`),
			body:              anthropicschema.MessagesRequest{Stream: true, Model: "claude-2"},
			forceBodyMutation: false,
			modelNameOverride: "claude-100.1",
			prefix:            "v1",
			expRequestModel:   "claude-100.1",
			expNewBody:        []byte(`{"model":"claude-100.1","messages":[{"role":"user","content":"Hello!"}], Stream: true}`),
			expPath:           "/v1/messages",
		},
		{
			name:              "force mutation",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			body:              anthropicschema.MessagesRequest{Stream: false, Model: "claude-2"},
			forceBodyMutation: true,
			modelNameOverride: "",
			prefix:            "v1",
			expRequestModel:   "claude-2",
			expNewBody:        []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			expPath:           "/v1/messages",
		},
		{
			name:              "empty prefix yields /messages",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			body:              anthropicschema.MessagesRequest{Stream: false, Model: "claude-2"},
			forceBodyMutation: false,
			modelNameOverride: "",
			prefix:            "",
			expRequestModel:   "claude-2",
			expNewBody:        nil,
			expPath:           "/messages",
		},
		{
			name:              "custom prefix",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			body:              anthropicschema.MessagesRequest{Stream: false, Model: "claude-2"},
			forceBodyMutation: false,
			modelNameOverride: "",
			prefix:            "gateway/v1",
			expRequestModel:   "claude-2",
			expNewBody:        nil,
			expPath:           "/gateway/v1/messages",
		},
		{
			name:              "custom prefix with leading slash",
			original:          []byte(`{"model":"claude-2","messages":[{"role":"user","content":"Hello!"}]}`),
			body:              anthropicschema.MessagesRequest{Stream: false, Model: "claude-2"},
			forceBodyMutation: false,
			modelNameOverride: "",
			prefix:            "/custom",
			expRequestModel:   "claude-2",
			expNewBody:        nil,
			expPath:           "/custom/messages",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := NewAnthropicToAnthropicTranslator(tc.prefix, tc.modelNameOverride)
			require.NotNil(t, translator)

			headerMutation, bodyMutation, err := translator.RequestBody(tc.original, &tc.body, tc.forceBodyMutation)
			require.NoError(t, err)
			expHeaders := []internalapi.Header{
				{pathHeaderName, tc.expPath},
			}
			if bodyMutation != nil {
				expHeaders = append(expHeaders, internalapi.Header{contentLengthHeaderName, strconv.Itoa(len(bodyMutation))})
			}
			require.Equal(t, expHeaders, headerMutation)
			require.Equal(t, tc.expNewBody, bodyMutation)

			require.Equal(t, tc.expRequestModel, translator.(*anthropicToAnthropicTranslator).requestModel)
			require.Equal(t, tc.body.Stream, translator.(*anthropicToAnthropicTranslator).stream)
		})
	}
}

func TestAnthropicToAnthropic_ResponseHeaders(t *testing.T) {
	translator := NewAnthropicToAnthropicTranslator("", "")
	require.NotNil(t, translator)

	headerMutation, err := translator.ResponseHeaders(nil)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
}

func TestAnthropicToAnthropic_ResponseBody_non_streaming(t *testing.T) {
	translator := NewAnthropicToAnthropicTranslator("", "")
	require.NotNil(t, translator)
	const responseBody = `{"model":"claude-sonnet-4-5-20250929","id":"msg_01J5gW6Sffiem6avXSAooZZw","type":"message","role":"assistant","content":[{"type":"text","text":"Hi! 👋 How can I help you today?"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":16,"service_tier":"standard"}}`

	headerMutation, bodyMutation, tokenUsage, responseModel, err := translator.ResponseBody(nil, strings.NewReader(responseBody), true, nil)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	expected := tokenUsageFrom(9, 0, 0, 16, 25, -1)
	require.Equal(t, expected, tokenUsage)
	require.Equal(t, "claude-sonnet-4-5-20250929", responseModel)
}

func TestAnthropicToAnthropic_ResponseBody_streaming(t *testing.T) {
	translator := NewAnthropicToAnthropicTranslator("", "")
	require.NotNil(t, translator)
	translator.(*anthropicToAnthropicTranslator).stream = true

	// We split the response into two parts to simulate streaming where each part can end in the
	// middle of an event.
	const responseHead = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_01BfvfMsg2gBzwsk6PZRLtDg","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":1,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":0,"service_tier":"standard"}}    }

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}      }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}           }

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"! 👋 How"}      }

`

	const responseTail = `
event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" can I help you today?"}   }

event: content_block_stop
data: {"type":"content_block_stop","index":0             }

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":16}               }

event: message_stop
data: {"type":"message_stop"       }`

	headerMutation, bodyMutation, tokenUsage, responseModel, err := translator.ResponseBody(nil, strings.NewReader(responseHead), false, nil)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	expected := tokenUsageFrom(10, 1, 0, 0, 10, -1)
	require.Equal(t, expected, tokenUsage)
	require.Equal(t, "claude-sonnet-4-5-20250929", responseModel)

	headerMutation, bodyMutation, tokenUsage, responseModel, err = translator.ResponseBody(nil, strings.NewReader(responseTail), false, nil)
	require.NoError(t, err)
	require.Nil(t, headerMutation)
	require.Nil(t, bodyMutation)
	expected = tokenUsageFrom(10, 1, 0, 16, 26, -1)
	require.Equal(t, expected, tokenUsage)
	require.Equal(t, "claude-sonnet-4-5-20250929", responseModel)
}

func TestAnthropicToAnthropic_ResponseBody_streaming_usageOnMessageDelta(t *testing.T) {
	// Some Anthropic-compatible streaming backends report the final input/cache usage only on the
	// message_delta event rather than message_start. The translator must merge those fields instead
	// of dropping everything but output_tokens. See https://github.com/envoyproxy/ai-gateway/issues/2290.
	t.Run("cache creation tokens", func(t *testing.T) {
		translator := NewAnthropicToAnthropicTranslator("", "")
		require.NotNil(t, translator)
		translator.(*anthropicToAnthropicTranslator).stream = true

		const responseBody = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_x","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":4522,"output_tokens":5,"cache_creation_input_tokens":4511}}

event: message_stop
data: {"type":"message_stop"}`

		_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(responseBody), false, nil)
		require.NoError(t, err)
		// Total input = input_tokens(4522) + cache_creation_input_tokens(4511) = 9033; total = 9033 + 5 = 9038.
		require.Equal(t, tokenUsageFrom(9033, 0, 4511, 5, 9038, -1), tokenUsage)
	})

	t.Run("cache read tokens", func(t *testing.T) {
		translator := NewAnthropicToAnthropicTranslator("", "")
		require.NotNil(t, translator)
		translator.(*anthropicToAnthropicTranslator).stream = true

		const responseBody = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_x","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":4522,"output_tokens":5,"cache_read_input_tokens":4511}}

event: message_stop
data: {"type":"message_stop"}`

		_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(responseBody), false, nil)
		require.NoError(t, err)
		// Total input = input_tokens(4522) + cache_read_input_tokens(4511) = 9033; total = 9033 + 5 = 9038.
		require.Equal(t, tokenUsageFrom(9033, 4511, 0, 5, 9038, -1), tokenUsage)
	})

	t.Run("partial fields on message_delta do not clobber message_start", func(t *testing.T) {
		// A backend may set input_tokens on message_start and only report the final cache_read on
		// message_delta. Merging must be per field: the delta omits input_tokens (reported as 0),
		// which must not zero out the value already recorded from message_start.
		translator := NewAnthropicToAnthropicTranslator("", "")
		require.NotNil(t, translator)
		translator.(*anthropicToAnthropicTranslator).stream = true

		const responseBody = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_x","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1000,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":10,"cache_read_input_tokens":500}}

event: message_stop
data: {"type":"message_stop"}`

		_, _, tokenUsage, _, err := translator.ResponseBody(nil, strings.NewReader(responseBody), false, nil)
		require.NoError(t, err)
		// message_start raw input(1000) is preserved; delta adds cache_read(500).
		// Total input = 1000 + 500 = 1500; total = 1500 + 10 = 1510.
		require.Equal(t, tokenUsageFrom(1500, 500, 0, 10, 1510, -1), tokenUsage)
	})
}

func TestAnthropicToAnthropic_ResponseError(t *testing.T) {
	t.Run("json error", func(t *testing.T) {
		translator := NewAnthropicToAnthropicTranslator("", "")
		require.NotNil(t, translator)
		hdrs, body, err := translator.ResponseError(map[string]string{
			"content-type": "application/json",
		}, strings.NewReader(`{"error":{"code":"invalid_request_error","message":"The model 'claude-unknown' does not exist."}}`))
		require.Nil(t, hdrs)
		require.Nil(t, body)
		require.NoError(t, err)
	})
	for _, tc := range []struct {
		statusCode int
		expType    string
	}{
		{400, "invalid_request_error"},
		{401, "authentication_error"},
		{403, "permission_error"},
		{404, "not_found_error"},
		{429, "rate_limit_error"},
		{500, "internal_server_error"},
		{503, "service_unavailable_error"},
	} {
		t.Run("non-json error "+strconv.Itoa(tc.statusCode), func(t *testing.T) {
			translator := NewAnthropicToAnthropicTranslator("", "")
			require.NotNil(t, translator)
			hdrs, body, err := translator.ResponseError(map[string]string{
				"content-type": "text/plain",
				":status":      strconv.Itoa(tc.statusCode),
			}, strings.NewReader("Some error occurred"))
			require.NoError(t, err)
			require.Len(t, hdrs, 2)
			require.Equal(t, "application/json", hdrs[0].Value())
			require.Equal(t, strconv.Itoa(len(body)), hdrs[1].Value())
			var resp anthropicschema.ErrorResponse
			err = json.Unmarshal(body, &resp)
			require.NoError(t, err)
			require.Equal(t, "error", resp.Type)
			require.Equal(t, tc.expType, resp.Error.Type)
			require.Equal(t, "Some error occurred", resp.Error.Message)
		})
	}
}

// The space after "data:" is optional per the SSE specification, and some
// Anthropic-compatible backends omit it. The stream must still yield usage.
func TestAnthropicToAnthropic_ResponseBody_streaming_noSpaceAfterColon(t *testing.T) {
	translator := NewAnthropicToAnthropicTranslator("", "")
	require.NotNil(t, translator)
	translator.(*anthropicToAnthropicTranslator).stream = true

	const response = `event: message_start
data:{"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_01","type":"message","role":"assistant","content":[],"usage":{"input_tokens":9,"cache_creation_input_tokens":0,"cache_read_input_tokens":1,"output_tokens":0}}}

event: message_delta
data:{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":16}}

event: message_stop
data:{"type":"message_stop"}`

	_, _, tokenUsage, responseModel, err := translator.ResponseBody(nil, strings.NewReader(response), true, nil)
	require.NoError(t, err)
	require.Equal(t, tokenUsageFrom(10, 1, 0, 16, 26, -1), tokenUsage)
	require.Equal(t, "claude-sonnet-4-5-20250929", responseModel)
}
