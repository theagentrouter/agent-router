// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.opentelemetry.io/otel/propagation"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/tracing"
	"github.com/envoyproxy/ai-gateway/internal/tracing/openinference"
)

func TestAWSAnthropicChatCompletionSpanMatchesClientUsage(t *testing.T) {
	t.Setenv(tracing.EnvTracingSemConv, "openinference")
	t.Setenv(openinference.EnvHideOutputs, "false")
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	t.Setenv("OTEL_SDK_DISABLED", "false")

	const message = `{"id":"msg_test","model":"claude-test","type":"message","role":"assistant","content":%s,"stop_reason":%s,"usage":{"input_tokens":5,"cache_read_input_tokens":7,"cache_creation_input_tokens":20,"output_tokens":%d}}`
	complete := fmt.Sprintf(message, `[{"type":"text","text":"Hi"}]`, `"end_turn"`, 3)
	start := fmt.Sprintf(message, `[]`, `null`, 0)
	var frames [][]byte
	for _, event := range []string{
		`{"type":"message_start","message":` + start + `}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	} {
		frame, err := wrapAnthropicSSEInEventStream("data: " + event + "\n\n")
		require.NoError(t, err)
		frames = append(frames, frame)
	}

	for _, tc := range []struct {
		name   string
		stream bool
		bodies [][]byte
	}{
		{"complete", false, [][]byte{[]byte(complete)}},
		{"stream", true, frames},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var exported bytes.Buffer
			pipeline, err := tracing.NewTracingFromEnv(t.Context(), &exported, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pipeline.Shutdown(context.Background())) })
			req := &openai.ChatCompletionRequest{}
			require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-test","messages":[{"role":"user","content":"Say hi"}],"max_tokens":10}`), req))
			req.Stream = tc.stream
			if tc.stream {
				req.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
			}
			requestBody, err := json.Marshal(req)
			require.NoError(t, err)
			span := pipeline.ChatCompletionTracer().StartSpanAndInjectHeaders(t.Context(), nil, propagation.MapCarrier{}, req, requestBody)
			require.NotNil(t, span)
			tr := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
			_, _, err = tr.RequestBody(requestBody, req, false)
			require.NoError(t, err)
			var wire bytes.Buffer
			for i, body := range tc.bodies {
				_, translated, _, _, err := tr.ResponseBody(map[string]string{statusHeaderName: "200"}, bytes.NewReader(body), i == len(tc.bodies)-1, span)
				require.NoError(t, err)
				wire.Write(translated)
			}
			span.EndSpan()

			payload := wire.Bytes()
			if tc.stream {
				events := bytes.Split(bytes.TrimSpace(payload), []byte("\n\n"))
				require.GreaterOrEqual(t, len(events), 2)
				require.Equal(t, "data: [DONE]", string(events[len(events)-1]))
				var ok bool
				payload, ok = bytes.CutPrefix(events[len(events)-2], []byte("data: "))
				require.True(t, ok, "expected final usage event")
			}
			var response struct {
				Usage *openai.Usage `json:"usage"`
			}
			require.NoError(t, json.Unmarshal(payload, &response))
			u := response.Usage
			require.NotNil(t, u)
			require.NotNil(t, u.PromptTokensDetails)
			client := map[string]int64{
				openinference.LLMTokenCountPrompt:           int64(u.PromptTokens),
				openinference.LLMTokenCountCompletion:       int64(u.CompletionTokens),
				openinference.LLMTokenCountTotal:            int64(u.TotalTokens),
				openinference.LLMTokenCountPromptCacheWrite: int64(u.PromptTokensDetails.CacheWriteTokens),
			}
			require.Equal(t, map[string]int64{
				openinference.LLMTokenCountPrompt: 32, openinference.LLMTokenCountCompletion: 3,
				openinference.LLMTokenCountTotal: 35, openinference.LLMTokenCountPromptCacheWrite: 20,
			}, client, "translated client usage")

			require.True(t, gjson.ValidBytes(exported.Bytes()), "expected one finished span")
			recorded := gjson.ParseBytes(exported.Bytes())
			require.Equal(t, "ChatCompletion", recorded.Get("Name").String())
			output := recorded.Get(`Attributes.#(Key=="output.value").Value.Value`).String()
			require.Equal(t, "Hi", gjson.Get(output, "choices.0.message.content").String(), "exported assistant text")
			spanUsage := map[string]int64{}
			for _, attr := range recorded.Get("Attributes").Array() {
				key := attr.Get("Key").String()
				if _, relevant := client[key]; relevant {
					require.Equal(t, "INT64", attr.Get("Value.Type").String())
					spanUsage[key] = attr.Get("Value.Value").Int()
				}
			}
			t.Logf("client usage %v, exported span usage %v", client, spanUsage)
			require.Equal(t, client, spanUsage, "finished OpenInference span must match translated client usage")
		})
	}
}
