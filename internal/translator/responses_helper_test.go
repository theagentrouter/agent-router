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
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func requirePathHeader(t *testing.T, headers []internalapi.Header, expectedPath string) {
	t.Helper()
	var found bool
	for _, h := range headers {
		if h.Key() == pathHeaderName {
			found = true
			require.Equal(t, expectedPath, h.Value())
		}
	}
	require.True(t, found, "expected :path header to be present")
}

func requireJSONFields(t *testing.T, expectedJSON string, body []byte) {
	t.Helper()
	var actual, expected map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &actual))
	require.NoError(t, json.Unmarshal([]byte(expectedJSON), &expected))
	for key, value := range expected {
		require.JSONEq(t, string(value), string(actual[key]))
	}
}

func newResponsesChatCompletionTestTranslator() OpenAIResponsesTranslator {
	return NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToOpenAITranslator("v1", ""))
}

func TestResponsesViaChatCompletion_UnsupportedRequests(t *testing.T) {
	for _, tc := range []struct{ name, request string }{
		{"continuation", `{"previous_response_id":"resp_1","input":"Continue"}`},
		{"conversation ID", `{"conversation":"conv_1","input":"Continue"}`},
		{"conversation object", `{"conversation":{"id":"conv_1"},"input":"Continue"}`},
		{"background", `{"background":true}`},
		{"stored prompt", `{"prompt":{"id":"pmpt_1","variables":{"name":"Sam"}},"input":"Hello"}`},
		{"response storage", `{"store":true,"input":"Hello"}`},
		{"automatic truncation", `{"truncation":"auto"}`},
		{"context management", `{"context_management":[{"type":"compaction","compact_threshold":1000}]}`},
		{"reasoning summary", `{"reasoning":{"summary":"auto"}}`},
		{"legacy reasoning summary", `{"reasoning":{"generate_summary":"detailed"}}`},
		{"assistant image", `{"input":[{"role":"assistant","content":[{"type":"input_image","image_url":"https://example.com/image.png"}]}]}`},
		{"system image", `{"input":[{"role":"system","content":[{"type":"input_image","image_url":"https://example.com/image.png"}]}]}`},
		{"developer image", `{"input":[{"role":"developer","content":[{"type":"input_text","text":"Look"},{"type":"input_image","image_url":"https://example.com/image.png"}]}]}`},
		{"top logprobs", `{"top_logprobs":5}`},
		{"zero top logprobs", `{"top_logprobs":0}`},
		{"included logprobs", `{"include":["message.output_text.logprobs"]}`},
		{"encrypted reasoning include", `{"include":["reasoning.encrypted_content"]}`},
		{"reasoning item", `{"input":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"encrypted"}]}`},
		{"item reference", `{"input":[{"type":"item_reference","id":"msg_1"}]}`},
		{"hosted tool call", `{"input":[{"type":"file_search_call","id":"fs_1","queries":["test"],"status":"completed"}]}`},
		{"web search", `{"tools":[{"type":"web_search"}]}`},
		{"file search", `{"tools":[{"type":"file_search","vector_store_ids":["vs_1"]}]}`},
		{"mixed tools", `{"tools":[{"type":"function","name":"weather"},{"type":"web_search"}]}`},
		{"file only", `{"input":[{"role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]}`},
		{"text and file", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Summarize this"},{"type":"input_file","file_id":"file_1"}]}]}`},
		{"system file", `{"input":[{"type":"message","role":"system","content":[{"type":"input_file","file_id":"file_1"}]}]}`},
		{"developer file", `{"input":[{"role":"developer","content":[{"type":"input_file","file_id":"file_1"}]}]}`},
		{"image file ID", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`},
		{"typed image file ID", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`},
		{"mixed image file ID", `{"input":[{"role":"user","content":[{"type":"input_text","text":"Describe this"},{"type":"input_image","file_id":"file_1"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(tc.request), &req))
			headers, body, err := newResponsesChatCompletionTestTranslator().RequestBody(nil, &req, false)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
			require.Nil(t, headers)
			require.Nil(t, body)
		})
	}
}

func TestResponsesViaChatCompletion_SchemaLimits(t *testing.T) {
	cycle := map[string]any{"type": "object"}
	cycle["properties"] = map[string]any{"child": cycle}
	properties := make(map[string]any, 50001)
	for i := range 50001 {
		properties[fmt.Sprintf("field%d", i)] = map[string]any{"type": "object"}
	}
	for _, tc := range []struct {
		name       string
		parameters map[string]any
	}{
		{"cyclic", cycle},
		{"node limit", map[string]any{"type": "object", "properties": properties}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := newResponsesChatCompletionTestTranslator().RequestBody(nil, &openai.ResponseRequest{
				Tools: []openai.ResponseToolUnion{{OfFunction: &openai.FunctionToolParam{Name: "test", Parameters: tc.parameters}}},
			}, false)
			require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
		})
	}
}

func TestResponsesViaChatCompletion_Reasoning(t *testing.T) {
	for _, provider := range []string{"anthropic", "gemini", "openai"} {
		for _, mode := range []string{"non-stream", "stream", "split-stream"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				streaming := mode != "non-stream"
				inner := NewChatCompletionOpenAIToOpenAITranslator("v1", "")
				body := `{"id":"chat_1","model":"backend","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"private plan","content":"answer"},"finish_reason":"stop"}]}`
				if streaming {
					body = "data: {\"id\":\"chat_1\",\"model\":\"backend\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private plan\"}}]}\n\n" +
						"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n"
				}
				switch provider {
				case "anthropic":
					inner = NewChatCompletionOpenAIToGCPAnthropicTranslator("v1", "")
					body = `{"id":"msg_1","type":"message","model":"claude","role":"assistant","content":[{"type":"thinking","thinking":"private plan","signature":"sig"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
					if streaming {
						body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
							"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private plan\"}}\n\n" +
							"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n" +
							"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
							"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
				case "gemini":
					inner = NewChatCompletionOpenAIToGCPVertexAITranslator("")
					body = `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"private plan","thought":true},{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}` + "\n"
				}
				translator := NewResponsesViaChatCompletionTranslator(inner)
				_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: streaming, Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")}}, false)
				require.NoError(t, err)
				span := &responsesRecordingSpan{}
				step := len(body)
				if mode == "split-stream" {
					step = 1
				}
				var output []byte
				for offset := 0; offset < len(body); offset += step {
					end := min(offset+step, len(body))
					_, converted, _, _, err := translator.ResponseBody(nil, strings.NewReader(body[offset:end]), end == len(body), span)
					require.NoError(t, err)
					output = append(output, converted...)
				}
				var response openai.Response
				if streaming {
					events := parseSSEEventsFromBytes(output)
					var reasoning, answer string
					for i, event := range span.events {
						if added := event.OfResponseOutputItemAdded; added != nil && added.Item.OfReasoning != nil {
							require.Len(t, added.Item.OfReasoning.Content, 1)
							require.Empty(t, added.Item.OfReasoning.Content[0].Text)
						}
						if delta := event.OfResponseReasoningTextDelta; delta != nil {
							reasoning += delta.Delta
							require.Equal(t, int64(0), delta.OutputIndex)
						}
						if delta := event.OfResponseTextDelta; delta != nil {
							answer += delta.Delta
							require.Equal(t, int64(1), delta.OutputIndex)
						}
						requireJSONFields(t, fmt.Sprintf(`{"sequence_number":%d}`, i), []byte(events[i].data))
					}
					require.Equal(t, "private plan", reasoning)
					require.Equal(t, "answer", answer)
					require.Contains(t, string(output), "event: response.reasoning_text.done")
					response = span.events[len(span.events)-1].OfResponseCompleted.Response
				} else {
					require.NoError(t, json.Unmarshal(output, &response))
				}
				require.Len(t, response.Output, 2)
				require.NotNil(t, response.Output[0].OfReasoning)
				require.NotNil(t, response.Output[0].OfReasoning.Summary)
				require.Equal(t, "private plan", response.Output[0].OfReasoning.Content[0].Text)
				require.Equal(t, "answer", response.Output[1].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
			})
		}
	}
}

func TestResponsesViaChatCompletion_Refusal(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		stream, text, refusalFirst bool
	}{
		{"non-stream", false, false, false},
		{"non-stream mixed", false, true, false},
		{"stream", true, false, false},
		{"stream mixed", true, true, false},
		{"stream refusal first", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			translator := newResponsesChatCompletionTestTranslator()
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: tc.stream}, false)
			require.NoError(t, err)
			content := "null"
			if tc.text {
				content = `"answer"`
			}
			body := `{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":` + content + `,"refusal":"not allowed"},"finish_reason":"stop"}]}`
			if tc.stream {
				deltas := []string{`{"refusal":"not "}`, `{"refusal":"allowed"}`}
				if tc.text {
					if tc.refusalFirst {
						deltas = append(deltas, `{"content":"answer"}`)
					} else {
						deltas = append([]string{`{"content":"answer"}`}, deltas...)
					}
				}
				body = ""
				for _, delta := range deltas {
					body += "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":" + delta + "}]}\n\n"
				}
				body += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
			}
			span := &responsesRecordingSpan{}
			step := len(body)
			if tc.stream {
				step = 1
			}
			var output []byte
			for offset := 0; offset < len(body); offset += step {
				end := min(offset+step, len(body))
				_, converted, _, _, err := translator.ResponseBody(nil, strings.NewReader(body[offset:end]), end == len(body), span)
				require.NoError(t, err)
				output = append(output, converted...)
			}
			var response openai.Response
			refusalIndex := 0
			if tc.text && !tc.refusalFirst {
				refusalIndex = 1
			}
			if tc.stream {
				var refusal string
				var done bool
				for _, event := range span.events {
					if delta := event.OfResponseRefusalDelta; delta != nil {
						refusal += delta.Delta
						require.Equal(t, int64(refusalIndex), delta.ContentIndex)
					}
					if end := event.OfResponseRefusalDone; end != nil {
						done = true
						require.Equal(t, "not allowed", end.Refusal)
						require.Equal(t, int64(refusalIndex), end.ContentIndex)
					}
				}
				require.Equal(t, "not allowed", refusal)
				require.True(t, done)
				response = span.events[len(span.events)-1].OfResponseCompleted.Response
				if !tc.text {
					require.NotContains(t, string(output), "event: response.output_text.")
				}
			} else {
				require.NoError(t, json.Unmarshal(output, &response))
				require.Len(t, span.responses, 1)
			}
			require.Equal(t, "completed", response.Status)
			require.False(t, *response.Store)
			require.Len(t, response.Output, 1)
			parts := response.Output[0].OfOutputMessage.Content.OfContentArray
			require.Equal(t, "not allowed", parts[refusalIndex].OfRefusal.Refusal)
			if tc.text {
				require.Len(t, parts, 2)
				require.Equal(t, "answer", parts[1-refusalIndex].OfOutputText.Text)
			} else {
				require.Len(t, parts, 1)
			}
		})
	}
}

func TestResponsesViaChatCompletion_AnthropicSettings(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		for _, limit := range []*int64{nil, ptr.To(int64(128))} {
			t.Run(fmt.Sprintf("%s/limit=%v", provider, ptr.Deref(limit, 4096)), func(t *testing.T) {
				inner := NewChatCompletionOpenAIToAWSAnthropicTranslator("", "")
				if provider == "gcp" {
					inner = NewChatCompletionOpenAIToGCPAnthropicTranslator("v1", "")
				}
				translator := NewResponsesViaChatCompletionTranslator(inner)
				var req openai.ResponseRequest
				require.NoError(t, json.Unmarshal([]byte(`{"model":"claude-sonnet-4-6","input":"Hello","reasoning":{"effort":"high"},"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`), &req))
				req.MaxOutputTokens = limit
				_, body, err := translator.RequestBody(nil, &req, false)
				require.NoError(t, err)
				var params struct {
					MaxTokens int64 `json:"max_tokens"`
					Tools     []struct {
						Strict      bool `json:"strict"`
						InputSchema struct {
							Required []string `json:"required"`
						} `json:"input_schema"`
					} `json:"tools"`
					OutputConfig struct {
						Effort string `json:"effort"`
					} `json:"output_config"`
				}
				require.NoError(t, json.Unmarshal(body, &params))
				require.Equal(t, ptr.Deref(limit, 4096), params.MaxTokens)
				require.Len(t, params.Tools, 1)
				require.True(t, params.Tools[0].Strict)
				require.Equal(t, []string{"city"}, params.Tools[0].InputSchema.Required)
				require.Equal(t, "high", params.OutputConfig.Effort)
				require.Equal(t, limit, req.MaxOutputTokens)
			})
		}
	}
}

func TestResponsesViaChatCompletion_GeminiSettings(t *testing.T) {
	translator := NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToGCPVertexAITranslator(""))
	var req openai.ResponseRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"gemini-3-flash","input":"Hello","reasoning":{"effort":"high"}}`), &req))
	_, body, err := translator.RequestBody(nil, &req, false)
	require.NoError(t, err)
	var params struct {
		GenerationConfig struct {
			MaxOutputTokens *int64 `json:"maxOutputTokens"`
			ThinkingConfig  struct {
				ThinkingLevel string `json:"thinkingLevel"`
			} `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	require.NoError(t, json.Unmarshal(body, &params))
	require.Equal(t, "HIGH", params.GenerationConfig.ThinkingConfig.ThinkingLevel)
	require.Nil(t, params.GenerationConfig.MaxOutputTokens)
}

func TestResponsesViaChatCompletion_GeminiModelError(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			translator := NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToGCPVertexAITranslator(""))
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "gemini-3-flash", Stream: stream, Input: openai.ResponseNewParamsInputUnion{OfString: ptr.To("Hello")}}, false)
			require.NoError(t, err)
			body := `{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"OTHER"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}` + "\n"
			span := &responsesRecordingSpan{}
			_, translated, _, _, err := translator.ResponseBody(nil, strings.NewReader(body), true, span)
			require.NoError(t, err)
			var response openai.Response
			if stream {
				events := parseSSEEventsFromBytes(translated)
				require.Equal(t, "response.failed", events[len(events)-1].eventType)
				require.NotNil(t, span.events[len(span.events)-1].OfResponseFailed)
				response = span.events[len(span.events)-1].OfResponseFailed.Response
			} else {
				require.NoError(t, json.Unmarshal(translated, &response))
			}
			require.Equal(t, "failed", response.Status)
			require.Equal(t, "server_error", response.Error.Code)
			require.Equal(t, "incomplete", response.Output[0].OfOutputMessage.Status)
		})
	}
}

func requireReplayableResponsesOutput(t *testing.T, output []openai.ResponseOutputItemUnion) {
	t.Helper()
	encoded, err := json.Marshal(output)
	require.NoError(t, err)
	var input []openai.ResponseInputItemUnionParam
	require.NoError(t, json.Unmarshal(encoded, &input))
	var calls []string
	for i := range output {
		if tool := output[i].OfFunctionCall; tool != nil {
			calls = append(calls, tool.CallID)
			input = append(input, openai.ResponseInputItemUnionParam{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: tool.CallID, Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: ptr.To("done")},
			}})
		}
	}
	chat, err := responsesRequestToChatCompletionRequest(&openai.ResponseRequest{Input: openai.ResponseNewParamsInputUnion{OfInputItemList: input}}, "model")
	require.NoError(t, err)
	require.Len(t, chat.Messages, len(calls)+1)
	require.NotNil(t, chat.Messages[0].OfAssistant)
	require.Len(t, chat.Messages[0].OfAssistant.ToolCalls, len(calls))
	for i, call := range calls {
		require.Equal(t, call, chat.Messages[i+1].OfTool.ToolCallID)
	}
}

func TestResponsesViaChatCompletion_OutputOrder(t *testing.T) {
	for _, textFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("textFirst=%t", textFirst), func(t *testing.T) {
			translator := newResponsesChatCompletionTestTranslator()
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: true}, false)
			require.NoError(t, err)
			deltas := []string{
				`{"tool_calls":[{"index":7,"id":"call_7","function":{"name":"weather","arguments":"{"}}]}`,
				`{"tool_calls":[{"index":2,"id":"call_2","function":{"name":"weather","arguments":"{}"}}]}`,
			}
			ids := []string{"call_7", "call_2", "msg_chat_1"}
			text := `{"content":"hello"}`
			if textFirst {
				deltas = append([]string{text}, deltas...)
				ids = []string{"msg_chat_1", "call_7", "call_2"}
			} else {
				deltas = append(deltas, text)
			}
			deltas = append(deltas, `{"tool_calls":[{"index":7,"function":{"arguments":"}"}}]}`)
			var stream string
			for _, delta := range deltas {
				stream += "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":" + delta + "}]}\n\n"
			}
			stream += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"
			_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream), true, nil)
			require.NoError(t, err)
			var added, done []string
			events := parseSSEEventsFromBytes(body)
			for i, event := range events {
				var payload struct {
					OutputIndex    int64  `json:"output_index"`
					SequenceNumber int64  `json:"sequence_number"`
					ItemID         string `json:"item_id"`
					Item           struct {
						ID string `json:"id"`
					} `json:"item"`
				}
				require.NoError(t, json.Unmarshal([]byte(event.data), &payload))
				require.Equal(t, int64(i), payload.SequenceNumber)
				id := payload.ItemID
				if payload.Item.ID != "" {
					id = payload.Item.ID
				}
				if id != "" {
					require.GreaterOrEqual(t, payload.OutputIndex, int64(0))
					require.Less(t, payload.OutputIndex, int64(len(ids)))
					require.Equal(t, ids[payload.OutputIndex], id)
				}
				if event.eventType == "response.output_item.added" {
					added = append(added, id)
				}
				if event.eventType == "response.output_item.done" {
					done = append(done, id)
				}
			}
			require.Equal(t, ids, added)
			require.Equal(t, ids, done)
			var terminal openai.ResponseCompletedEvent
			require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &terminal))
			require.Len(t, terminal.Response.Output, len(ids))
			for i := range terminal.Response.Output {
				item := &terminal.Response.Output[i]
				if item.OfFunctionCall != nil {
					require.Equal(t, ids[i], item.OfFunctionCall.ID)
					require.JSONEq(t, `{}`, item.OfFunctionCall.Arguments)
				} else {
					require.Equal(t, ids[i], item.OfOutputMessage.ID)
				}
			}
			requireReplayableResponsesOutput(t, terminal.Response.Output)
		})
	}
	response := chatCompletionToResponse(&openai.ChatCompletionResponse{ID: "chat_1", Choices: []openai.ChatCompletionResponseChoice{{
		FinishReason: openai.ChatCompletionChoicesFinishReasonToolCalls,
		Message: openai.ChatCompletionResponseChoiceMessage{
			Content:   ptr.To("hello"),
			ToolCalls: []openai.ChatCompletionMessageToolCallParam{{ID: ptr.To("call_1"), Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "weather", Arguments: `{}`}}},
		},
	}}}, "model")
	require.NotNil(t, response.Output[0].OfOutputMessage)
	requireReplayableResponsesOutput(t, response.Output)
}

func TestResponsesViaChatCompletion_EmptyOutput(t *testing.T) {
	response := chatCompletionToResponse(&openai.ChatCompletionResponse{}, "model")
	body, err := json.Marshal(response)
	require.NoError(t, err)
	requireJSONFields(t, `{"output":[]}`, body)
	translator := newResponsesChatCompletionTestTranslator()
	_, _, err = translator.RequestBody(nil, &openai.ResponseRequest{Stream: true}, false)
	require.NoError(t, err)
	_, body, _, _, err = translator.ResponseBody(nil, strings.NewReader("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"), true, nil)
	require.NoError(t, err)
	events := parseSSEEventsFromBytes(body)
	require.Len(t, events, 3)
	for _, event := range events {
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(event.data), &payload))
		requireJSONFields(t, `{"output":[]}`, payload["response"])
	}
}

type responsesRecordingSpan struct {
	responses []*openai.Response
	events    []*openai.ResponseStreamEventUnion
}

func (s *responsesRecordingSpan) RecordResponse(resp *openai.Response) {
	s.responses = append(s.responses, resp)
}

func (s *responsesRecordingSpan) RecordResponseChunk(event *openai.ResponseStreamEventUnion) {
	s.events = append(s.events, event)
}

func (*responsesRecordingSpan) EndSpanOnError(int, []byte) {}
func (*responsesRecordingSpan) EndSpan()                   {}

func TestResponsesViaChatCompletion_ResponseDetails(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, cacheField := range []string{"cache_write_tokens", "cache_creation_input_tokens"} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, cacheField), func(t *testing.T) {
				var req openai.ResponseRequest
				require.NoError(t, json.Unmarshal([]byte(`{"model":"model","temperature":0.4,"top_p":0.8,"text":{"format":{"type":"json_schema","name":"answer","schema":{"type":"object"}}}}`), &req))
				req.Stream = stream
				translator := newResponsesChatCompletionTestTranslator()
				_, _, err := translator.RequestBody(nil, &req, false)
				require.NoError(t, err)
				usage := fmt.Sprintf(`{"prompt_tokens":12,"completion_tokens":6,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3,%q:4},"completion_tokens_details":{"reasoning_tokens":2}}`, cacheField)
				body := `{"id":"chat_1","model":"model","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}],"usage":` + usage + `}`
				if stream {
					body = "data: {\"id\":\"chat_1\",\"model\":\"model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n" +
						"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
						"data: {\"choices\":[],\"usage\":" + usage + "}\n\n"
				}
				span := &responsesRecordingSpan{}
				_, translated, _, _, err := translator.ResponseBody(nil, strings.NewReader(body), true, span)
				require.NoError(t, err)
				var response openai.Response
				if stream {
					events := parseSSEEventsFromBytes(translated)
					require.Len(t, span.events, len(events))
					require.Empty(t, span.responses)
					for i, event := range events {
						require.Equal(t, event.eventType, span.events[i].GetEventType())
					}
					require.NotNil(t, span.events[0].OfResponseCreated)
					require.NotNil(t, span.events[len(events)-1].OfResponseCompleted)
					response = span.events[len(events)-1].OfResponseCompleted.Response
					require.Equal(t, req.Text.Format, span.events[0].OfResponseCreated.Response.Text.Format)
					require.InDelta(t, 0.4, span.events[0].OfResponseCreated.Response.Temperature, 0.001)
					require.InDelta(t, 0.8, span.events[0].OfResponseCreated.Response.TopP, 0.001)
				} else {
					require.Len(t, span.responses, 1)
					require.Empty(t, span.events)
					require.NoError(t, json.Unmarshal(translated, &response))
					recorded, err := json.Marshal(span.responses[0])
					require.NoError(t, err)
					require.JSONEq(t, string(translated), string(recorded))
				}
				require.InDelta(t, 0.4, response.Temperature, 0.001)
				require.InDelta(t, 0.8, response.TopP, 0.001)
				require.Equal(t, req.Text.Format, response.Text.Format)
				require.Equal(t, int64(12), response.Usage.InputTokens)
				require.Equal(t, int64(6), response.Usage.OutputTokens)
				require.Equal(t, int64(18), response.Usage.TotalTokens)
				require.Equal(t, int64(3), response.Usage.InputTokensDetails.CachedTokens)
				require.Equal(t, int64(4), response.Usage.InputTokensDetails.CacheWriteTokensValue())
				require.Equal(t, int64(2), response.Usage.OutputTokensDetails.ReasoningTokens)
			})
		}
	}
}

func TestResponsesViaChatCompletion_StreamErrors(t *testing.T) {
	for _, errorType := range []string{"overloaded_error", "api_error", "rate_limit_error", "authentication_error"} {
		t.Run(errorType, func(t *testing.T) {
			for _, beforeContent := range []bool{false, true} {
				for _, split := range []bool{false, true} {
					t.Run(fmt.Sprintf("beforeContent=%t/split=%t", beforeContent, split), func(t *testing.T) {
						translator := NewResponsesViaChatCompletionTranslator(NewChatCompletionOpenAIToGCPAnthropicTranslator("v1", ""))
						_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "claude", Stream: true}, false)
						require.NoError(t, err)
						stream := fmt.Sprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":%q,\"message\":\"busy\"}}\n\n", errorType)
						if !beforeContent {
							stream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
								"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" + stream
						}
						step := len(stream)
						if split {
							step = 1
						}
						span := &responsesRecordingSpan{}
						var output []byte
						for offset := 0; offset < len(stream); offset += step {
							end := min(offset+step, len(stream))
							_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream[offset:end]), false, span)
							output = append(output, body...)
							if end == len(stream) {
								if errorType == "overloaded_error" {
									var overloaded *StreamOverloadedError
									require.ErrorAs(t, err, &overloaded)
								} else {
									var streamErr *StreamResponseError
									require.ErrorAs(t, err, &streamErr)
								}
							} else {
								require.NoError(t, err)
							}
						}
						events := parseSSEEventsFromBytes(output)
						require.NotEmpty(t, events)
						require.Equal(t, "error", events[len(events)-1].eventType)
						requireJSONFields(t, fmt.Sprintf(`{"type":"error","code":%q,"message":"busy"}`, errorType), []byte(events[len(events)-1].data))
						require.NotContains(t, string(output), `"choices"`)
						require.NotContains(t, string(output), `"error":{`)
						require.NotContains(t, string(output), "event: response.completed")
						require.Len(t, span.events, len(events))
						require.NotNil(t, span.events[len(events)-1].OfError)
						if !beforeContent {
							require.Contains(t, string(output), "event: response.output_text.delta")
						}
					})
				}
			}
		})
	}
}

func TestResponsesRequestToChatCompletionRequest_Controls(t *testing.T) {
	const parameters = `{"type":"object","properties":{"location":{"type":"object","properties":{"city":{"type":"string"}}},"days":{"type":"array","items":{"type":"object","properties":{"date":{"type":"string"}}}}}}`
	const strictParameters = `{"type":"object","additionalProperties":false,"required":["days","location"],"properties":{"location":{"type":"object","additionalProperties":false,"required":["city"],"properties":{"city":{"type":"string"}}},"days":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["date"],"properties":{"date":{"type":"string"}}}}}}`
	const incompatibleParameters = `{"type":"object","allOf":[{"properties":{"city":{"type":"string"}}}]}`
	for _, tc := range []struct {
		name, request, expected string
	}{
		{"json schema", `{"text":{"format":{"type":"json_schema","name":"answer","description":"An answer","strict":true,"schema":{"type":"object"}}}}`, `{"response_format":{"type":"json_schema","json_schema":{"name":"answer","description":"An answer","strict":true,"schema":{"type":"object"}}}}`},
		{"json object", `{"text":{"format":{"type":"json_object"}}}`, `{"response_format":{"type":"json_object"}}`},
		{"text", `{"text":{"format":{"type":"text"}}}`, `{"response_format":{"type":"text"}}`},
		{"required", `{"tool_choice":"required"}`, `{"tool_choice":"required"}`},
		{"none", `{"tool_choice":"none"}`, `{"tool_choice":"none"}`},
		{"auto", `{"tool_choice":"auto"}`, `{"tool_choice":"auto"}`},
		{"named function", `{"tool_choice":{"type":"function","name":"weather"}}`, `{"tool_choice":{"type":"function","function":{"name":"weather"}}}`},
		{"no parallel calls", `{"parallel_tool_calls":false}`, `{"parallel_tool_calls":false}`},
		{"parallel calls", `{"parallel_tool_calls":true}`, `{"parallel_tool_calls":true}`},
		{"foreground", `{"background":false,"input":"Hello"}`, `{"messages":[{"role":"user","content":"Hello"}]}`},
		{"stateless", `{"store":false,"input":"Hello"}`, `{"messages":[{"role":"user","content":"Hello"}]}`},
		{"no context changes", `{"truncation":"disabled","context_management":[],"input":"Hello"}`, `{"messages":[{"role":"user","content":"Hello"}]}`},
		{"null conversation", `{"conversation":null,"input":"Hello"}`, `{"messages":[{"role":"user","content":"Hello"}]}`},
		{"reasoning effort", `{"reasoning":{"effort":"high"}}`, `{"reasoning_effort":"high"}`},
		{"strict true", `{"tools":[{"type":"function","name":"weather","strict":true}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":null}}]}`},
		{"strict false", `{"tools":[{"type":"function","name":"weather","strict":false}]}`, `{"tools":[{"type":"function","function":{"name":"weather","parameters":null}}]}`},
		{"default no parameters", `{"tools":[{"type":"function","name":"weather"}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":{"type":"object","required":[],"additionalProperties":false}}}]}`},
		{"default strict", `{"tools":[{"type":"function","name":"weather","parameters":` + parameters + `}]}`, `{"tools":[{"type":"function","function":{"name":"weather","strict":true,"parameters":` + strictParameters + `}}]}`},
		{"incompatible default strict", `{"tools":[{"type":"function","name":"weather","parameters":` + incompatibleParameters + `}]}`, `{"tools":[{"type":"function","function":{"name":"weather","parameters":` + incompatibleParameters + `}}]}`},
		{"consecutive tool calls", `{"input":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{}"},{"type":"function_call","call_id":"call_2","name":"weather","arguments":"{}"}]}`, `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"type":"function","id":"call_1","function":{"name":"weather","arguments":"{}"}},{"type":"function","id":"call_2","function":{"name":"weather","arguments":"{}"}}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(tc.request), &req))
			original, err := json.Marshal(req.Tools)
			require.NoError(t, err)
			_, body, err := newResponsesChatCompletionTestTranslator().RequestBody(nil, &req, false)
			require.NoError(t, err)
			unchanged, err := json.Marshal(req.Tools)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(unchanged))
			requireJSONFields(t, tc.expected, body)
		})
	}
}

func TestResponsesRequestToChatCompletionRequest_ToolOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, output, expected string
		invalid                bool
	}{
		{"string", `"sunny"`, "sunny", false},
		{"text array", `[{"type":"input_text","text":"sunny"},{"type":"input_text","text":"20 degrees"}]`, "sunny\n\n20 degrees", false},
		{"empty array", `[]`, "", false},
		{"image", `[{"type":"input_image","image_url":"https://example.com/image.png"}]`, "", true},
		{"file", `[{"type":"input_file","file_id":"file_1"}]`, "", true},
		{"mixed", `[{"type":"input_text","text":"sunny"},{"type":"input_image","image_url":"https://example.com/image.png"}]`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var req openai.ResponseRequest
			require.NoError(t, json.Unmarshal([]byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":`+tc.output+`}]}`), &req))
			translator := newResponsesChatCompletionTestTranslator()
			_, body, err := translator.RequestBody(nil, &req, false)
			if tc.invalid {
				require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
				return
			}
			require.NoError(t, err)
			var chatReq openai.ChatCompletionRequest
			require.NoError(t, json.Unmarshal(body, &chatReq))
			require.Equal(t, tc.expected, chatReq.Messages[0].OfTool.Content.Value)
			require.Equal(t, "call_1", chatReq.Messages[0].OfTool.ToolCallID)
		})
	}
	t.Run("unsupported tool choice", func(t *testing.T) {
		var req openai.ResponseRequest
		require.NoError(t, json.Unmarshal([]byte(`{"tool_choice":{"type":"file_search"}}`), &req))
		_, _, err := newResponsesChatCompletionTestTranslator().RequestBody(nil, &req, false)
		require.ErrorIs(t, err, internalapi.ErrInvalidRequestBody)
	})
}

func TestResponsesViaChatCompletion_Buffering(t *testing.T) {
	for _, provider := range []string{"openai", "gemini", "anthropic"} {
		t.Run(provider, func(t *testing.T) {
			inner := NewChatCompletionOpenAIToOpenAITranslator("v1", "")
			stream := "data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\n"
			switch provider {
			case "gemini":
				inner = NewChatCompletionOpenAIToGCPVertexAITranslator("")
				stream = `{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}` + "\n"
			case "anthropic":
				inner = NewChatCompletionOpenAIToGCPAnthropicTranslator("v1", "")
				stream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			}
			translator := NewResponsesViaChatCompletionTranslator(inner)
			_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: true}, false)
			require.NoError(t, err)
			var output []byte
			for i := range len(stream) {
				_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream[i:i+1]), i == len(stream)-1, nil)
				require.NoError(t, err)
				require.NotNil(t, body, "buffered bytes must not pass through")
				output = append(output, body...)
			}
			events := parseSSEEventsFromBytes(output)
			require.NotEmpty(t, events)
			require.Equal(t, "response.created", events[0].eventType)
			require.Equal(t, "response.completed", events[len(events)-1].eventType)
			var terminal openai.ResponseCompletedEvent
			require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &terminal))
			require.Equal(t, "Hello", terminal.Response.Output[0].OfOutputMessage.Content.OfContentArray[0].OfOutputText.Text)
		})
	}
}

func TestResponsesViaChatCompletion_Termination(t *testing.T) {
	for _, reason := range []openai.ChatCompletionChoicesFinishReason{openai.ChatCompletionChoicesFinishReasonLength, openai.ChatCompletionChoicesFinishReasonContentFilter, openai.ChatCompletionChoicesFinishReasonError} {
		t.Run(string(reason), func(t *testing.T) {
			response := chatCompletionToResponse(&openai.ChatCompletionResponse{Choices: []openai.ChatCompletionResponseChoice{{
				FinishReason: reason,
				Message:      openai.ChatCompletionResponseChoiceMessage{Content: ptr.To("partial")},
			}}}, "model")
			expectedStatus := "incomplete"
			expectedReason := "max_output_tokens"
			if reason == openai.ChatCompletionChoicesFinishReasonContentFilter {
				expectedReason = "content_filter"
			}
			if reason == openai.ChatCompletionChoicesFinishReasonError {
				expectedStatus = "failed"
				expectedReason = ""
				require.Equal(t, "server_error", response.Error.Code)
			}
			require.Equal(t, expectedStatus, response.Status)
			require.Equal(t, expectedReason, response.IncompleteDetails.Reason)
			require.Equal(t, "incomplete", response.Output[0].OfOutputMessage.Status)
			for _, tool := range []bool{false, true} {
				for _, usage := range []bool{false, true} {
					t.Run(fmt.Sprintf("tool=%t/usage=%t", tool, usage), func(t *testing.T) {
						translator := newResponsesChatCompletionTestTranslator()
						_, _, err := translator.RequestBody(nil, &openai.ResponseRequest{Model: "model", Stream: true}, false)
						require.NoError(t, err)
						delta := `{"content":"partial"}`
						if tool {
							delta = `{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"weather","arguments":"{"}}]}`
						}
						stream := fmt.Sprintf("data: {\"id\":\"chat_1\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", delta, reason)
						if usage {
							stream += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"
						}
						_, body, _, _, err := translator.ResponseBody(nil, strings.NewReader(stream), true, nil)
						require.NoError(t, err)
						require.NotContains(t, string(body), "response.completed")
						events := parseSSEEventsFromBytes(body)
						require.Equal(t, "response."+expectedStatus, events[len(events)-1].eventType)
						var terminal struct {
							Type     string          `json:"type"`
							Response openai.Response `json:"response"`
						}
						require.NoError(t, json.Unmarshal([]byte(events[len(events)-1].data), &terminal))
						require.Equal(t, "response."+expectedStatus, terminal.Type)
						require.Equal(t, expectedStatus, terminal.Response.Status)
						require.Equal(t, expectedReason, terminal.Response.IncompleteDetails.Reason)
						if expectedStatus == "failed" {
							require.Equal(t, "server_error", terminal.Response.Error.Code)
						}
						var done openai.ResponseOutputItemDoneEvent
						require.NoError(t, json.Unmarshal([]byte(events[len(events)-2].data), &done))
						require.Equal(t, "response.output_item.done", done.Type)
						if tool {
							require.Equal(t, "incomplete", done.Item.OfFunctionCall.Status)
							require.Equal(t, "incomplete", terminal.Response.Output[0].OfFunctionCall.Status)
						} else {
							require.Equal(t, "incomplete", done.Item.OfOutputMessage.Status)
							require.Equal(t, "incomplete", terminal.Response.Output[0].OfOutputMessage.Status)
						}
					})
				}
			}
		})
	}
}

func TestResponsesRequestToChatCompletionRequest_ConversationWithTools(t *testing.T) {
	req := &openai.ResponseRequest{
		Model: "requested-model",
		Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
			{OfOutputMessage: &openai.ResponseOutputMessage{
				Role:    "assistant",
				Content: openai.ResponseOutputMessageContentUnion{OfString: ptr.To("I'll check the weather")},
			}},
			{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID: "call_1", Name: "weather", Arguments: `{"city":"Berlin"}`, Type: "function_call",
			}},
			{OfFunctionCallOutput: &openai.ResponseInputItemFunctionCallOutputParam{
				CallID: "call_1", Type: "function_call_output",
				Output: openai.ResponseInputItemFunctionCallOutputOutputUnionParam{OfString: ptr.To("sunny")},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role: "user",
				Content: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "What about this photo?"}},
					{OfInputImage: &openai.ResponseInputImageParam{ImageURL: "https://example.com/photo.png", Detail: "high"}},
				},
			}},
			{OfFunctionCall: &openai.ResponseFunctionToolCall{
				CallID: "call_2", Name: "describe", Arguments: `{}`, Type: "function_call",
			}},
		}},
	}

	chatReq, err := responsesRequestToChatCompletionRequest(req, "backend-model")
	require.NoError(t, err)
	require.Equal(t, "backend-model", chatReq.Model)
	require.Len(t, chatReq.Messages, 4)
	require.Equal(t, "I'll check the weather", chatReq.Messages[0].OfAssistant.Content.Value)
	require.Len(t, chatReq.Messages[0].OfAssistant.ToolCalls, 1)
	require.Equal(t, "call_1", *chatReq.Messages[0].OfAssistant.ToolCalls[0].ID)
	require.JSONEq(t, `{"city":"Berlin"}`, chatReq.Messages[0].OfAssistant.ToolCalls[0].Function.Arguments)
	require.Equal(t, "call_1", chatReq.Messages[1].OfTool.ToolCallID)
	require.Equal(t, "sunny", chatReq.Messages[1].OfTool.Content.Value)
	parts, ok := chatReq.Messages[2].OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam)
	require.True(t, ok)
	require.Len(t, parts, 2)
	require.Equal(t, "What about this photo?", parts[0].OfText.Text)
	require.Equal(t, "https://example.com/photo.png", parts[1].OfImageURL.ImageURL.URL)
	require.Equal(t, openai.ChatCompletionContentPartImageImageURLDetail("high"), parts[1].OfImageURL.ImageURL.Detail)
	require.Len(t, chatReq.Messages[3].OfAssistant.ToolCalls, 1)
	require.Equal(t, "call_2", *chatReq.Messages[3].OfAssistant.ToolCalls[0].ID)
}

func TestResponsesRequestToChatCompletionRequest_MessageRoles(t *testing.T) {
	req := &openai.ResponseRequest{
		Input: openai.ResponseNewParamsInputUnion{OfInputItemList: []openai.ResponseInputItemUnionParam{
			{OfMessage: &openai.EasyInputMessageParam{
				Role: openai.ChatMessageRoleSystem,
				Content: openai.EasyInputMessageContentUnionParam{OfInputItemContentList: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "Follow the rules"}},
					{OfInputText: &openai.ResponseInputTextParam{Text: "Keep it short"}},
				}},
			}},
			{OfMessage: &openai.EasyInputMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: openai.EasyInputMessageContentUnionParam{OfString: ptr.To("Use plain language")},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role: openai.ChatMessageRoleAssistant,
				Content: []openai.ResponseInputContentUnionParam{
					{OfInputText: &openai.ResponseInputTextParam{Text: "First answer"}},
					{OfInputText: &openai.ResponseInputTextParam{Text: "Second answer"}},
				},
			}},
			{OfInputMessage: &openai.ResponseInputItemMessageParam{
				Role:    openai.ChatMessageRoleDeveloper,
				Content: []openai.ResponseInputContentUnionParam{{OfInputText: &openai.ResponseInputTextParam{Text: "Be precise"}}},
			}},
			{OfOutputMessage: &openai.ResponseOutputMessage{
				Role: "assistant",
				Content: openai.ResponseOutputMessageContentUnion{OfContentArray: []openai.ResponseOutputMessageContentArrayUnion{
					{OfOutputText: &openai.ResponseOutputTextParam{Text: "I can't help with that"}},
					{OfRefusal: &openai.ResponseOutputRefusalParam{Refusal: "Not allowed"}},
				}},
			}},
		}},
	}

	chatReq, err := responsesRequestToChatCompletionRequest(req, "backend-model")
	require.NoError(t, err)
	require.Len(t, chatReq.Messages, 5)
	require.Equal(t, "Follow the rules\n\nKeep it short", chatReq.Messages[0].OfSystem.Content.Value)
	require.Equal(t, "Use plain language", chatReq.Messages[1].OfDeveloper.Content.Value)
	require.Equal(t, "First answer\n\nSecond answer", chatReq.Messages[2].OfAssistant.Content.Value)
	require.Equal(t, "Be precise", chatReq.Messages[3].OfDeveloper.Content.Value)
	require.Equal(t, "I can't help with that\n\nNot allowed", chatReq.Messages[4].OfAssistant.Content.Value)
}
