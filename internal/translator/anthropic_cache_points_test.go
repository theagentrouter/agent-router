// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"sort"
	"strconv"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"k8s.io/utils/ptr"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestAddAutomaticCachePoints(t *testing.T) {
	tool := func(name string) anthropic.ToolUnionParam {
		return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: name}}
	}
	cachedText := func(text string) anthropic.ContentBlockParamUnion {
		block := anthropic.NewTextBlock(text)
		block.OfText.CacheControl = anthropic.NewCacheControlEphemeralParam()
		return block
	}
	user := anthropic.NewUserMessage
	assistant := anthropic.NewAssistantMessage
	text := anthropic.NewTextBlock

	for _, tc := range []struct {
		name      string
		params    anthropic.MessageNewParams
		wantPaths []string
	}{
		{
			name: "tools, system and conversation",
			params: anthropic.MessageNewParams{
				Tools:  []anthropic.ToolUnionParam{tool("a"), tool("b")},
				System: []anthropic.TextBlockParam{{Text: "s1"}, {Text: "s2"}},
				Messages: []anthropic.MessageParam{
					user(text("q1")),
					assistant(text("a1")),
					user(text("q2"), anthropic.NewImageBlockBase64("image/png", "aGVsbG8=")),
				},
			},
			wantPaths: []string{
				"messages.2.content.1.cache_control",
				"system.1.cache_control",
				"tools.1.cache_control",
			},
		},
		{
			name:      "no tools and no system",
			params:    anthropic.MessageNewParams{Messages: []anthropic.MessageParam{user(text("q1"))}},
			wantPaths: []string{"messages.0.content.0.cache_control"},
		},
		{
			name: "last user turn is tool results",
			params: anthropic.MessageNewParams{
				Messages: []anthropic.MessageParam{
					user(text("q1")),
					assistant(text("a1"), anthropic.NewToolUseBlock("t1", map[string]any{}, "a"), anthropic.NewToolUseBlock("t2", map[string]any{}, "b")),
					user(anthropic.NewToolResultBlock("t1", "r1", false), anthropic.NewToolResultBlock("t2", "r2", false)),
				},
			},
			wantPaths: []string{"messages.2.content.1.cache_control"},
		},
		{
			name: "assistant prefill after the last user turn",
			params: anthropic.MessageNewParams{
				Messages: []anthropic.MessageParam{user(text("q1")), assistant(text("prefill"))},
			},
			wantPaths: []string{"messages.0.content.0.cache_control"},
		},
		{
			name: "last user turn without content",
			params: anthropic.MessageNewParams{
				System:   []anthropic.TextBlockParam{{Text: "s1"}},
				Messages: []anthropic.MessageParam{user(text("q1")), assistant(text("a1")), user()},
			},
			wantPaths: []string{"system.0.cache_control"},
		},
		{
			name: "no messages",
			params: anthropic.MessageNewParams{
				Tools:  []anthropic.ToolUnionParam{tool("a")},
				System: []anthropic.TextBlockParam{{Text: "s1"}},
			},
			wantPaths: []string{"system.0.cache_control", "tools.0.cache_control"},
		},
		{
			// Three positions, one breakpoint each, however many tools, system blocks and turns
			// there are, which keeps the request under Anthropic's limit of four.
			name: "one breakpoint per position",
			params: anthropic.MessageNewParams{
				Tools:  []anthropic.ToolUnionParam{tool("a"), tool("b"), tool("c"), tool("d"), tool("e")},
				System: []anthropic.TextBlockParam{{Text: "s1"}, {Text: "s2"}, {Text: "s3"}},
				Messages: []anthropic.MessageParam{
					user(text("q1"), text("q1b")), assistant(text("a1")),
					user(text("q2")), assistant(text("a2")),
					user(text("q3")), assistant(text("a3")),
					user(text("q4"), text("q4b"), text("q4c")),
				},
			},
			wantPaths: []string{
				"messages.6.content.2.cache_control",
				"system.2.cache_control",
				"tools.4.cache_control",
			},
		},
		{
			name: "client breakpoint on a tool",
			params: anthropic.MessageNewParams{
				Tools: []anthropic.ToolUnionParam{
					{OfTool: &anthropic.ToolParam{Name: "a", CacheControl: anthropic.NewCacheControlEphemeralParam()}},
					tool("b"),
				},
				System:   []anthropic.TextBlockParam{{Text: "s1"}},
				Messages: []anthropic.MessageParam{user(text("q1"))},
			},
			wantPaths: []string{"tools.0.cache_control"},
		},
		{
			name: "client breakpoint on a system block",
			params: anthropic.MessageNewParams{
				System: []anthropic.TextBlockParam{
					{Text: "s1", CacheControl: anthropic.NewCacheControlEphemeralParam()},
					{Text: "s2"},
				},
				Messages: []anthropic.MessageParam{user(text("q1"))},
			},
			wantPaths: []string{"system.0.cache_control"},
		},
		{
			name: "client breakpoint on an earlier turn",
			params: anthropic.MessageNewParams{
				System:   []anthropic.TextBlockParam{{Text: "s1"}},
				Messages: []anthropic.MessageParam{user(cachedText("q1")), assistant(text("a1")), user(text("q2"))},
			},
			wantPaths: []string{"messages.0.content.0.cache_control"},
		},
		{
			name: "client breakpoint inside tool result content",
			params: anthropic.MessageNewParams{
				System: []anthropic.TextBlockParam{{Text: "s1"}},
				Messages: []anthropic.MessageParam{
					user(text("q1")),
					assistant(anthropic.NewToolUseBlock("t1", map[string]any{}, "a")),
					user(anthropic.ContentBlockParamUnion{OfToolResult: &anthropic.ToolResultBlockParam{
						ToolUseID: "t1",
						Content: []anthropic.ToolResultBlockParamContentUnion{
							{OfText: &anthropic.TextBlockParam{Text: "r1", CacheControl: anthropic.NewCacheControlEphemeralParam()}},
						},
					}}),
				},
			},
			wantPaths: []string{"messages.2.content.0.content.0.cache_control"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addAutomaticCachePoints(&tc.params)
			body, err := json.Marshal(tc.params)
			require.NoError(t, err)
			paths := cacheControlPaths(body)
			require.Equal(t, tc.wantPaths, paths)
			require.LessOrEqual(t, len(paths), 4, "Anthropic accepts at most four breakpoints")
			for _, p := range paths {
				require.Equal(t, "ephemeral", gjson.GetBytes(body, p+".type").String(), p)
			}
		})
	}
}

func TestOpenAIToAnthropicTranslators_AutomaticCachePoints(t *testing.T) {
	newRequest := func(systemContent any) *openai.ChatCompletionRequest {
		return &openai.ChatCompletionRequest{
			Model:     "claude-sonnet-4-5",
			MaxTokens: ptr.To(int64(1024)),
			Tools: []openai.Tool{
				{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "get_weather"}},
				{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "get_time"}},
			},
			Messages: []openai.ChatCompletionMessageParamUnion{
				{OfSystem: &openai.ChatCompletionSystemMessageParam{
					Role:    openai.ChatMessageRoleSystem,
					Content: openai.ContentUnion{Value: systemContent},
				}},
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role:    openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{Value: "What's the weather in Paris?"},
				}},
				{OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Role:    openai.ChatMessageRoleAssistant,
					Content: openai.StringOrAssistantRoleContentUnion{Value: "Checking."},
					ToolCalls: []openai.ChatCompletionMessageToolCallParam{{
						ID:       ptr.To("call_1"),
						Type:     openai.ChatCompletionMessageToolCallTypeFunction,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{Name: "get_weather", Arguments: `{"city":"Paris"}`},
					}},
				}},
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:       openai.ChatMessageRoleTool,
					ToolCallID: "call_1",
					Content:    openai.ContentUnion{Value: "Sunny"},
				}},
			},
		}
	}
	// The client marks its own breakpoint on the system prompt.
	cachedSystem := []openai.ChatCompletionContentPartTextParam{{
		Type:                   "text",
		Text:                   "You are a helpful assistant.",
		AnthropicContentFields: &openai.AnthropicContentFields{CacheControl: anthropic.NewCacheControlEphemeralParam()},
	}}

	translators := map[string]func() OpenAIChatCompletionTranslator{
		"GCPAnthropic": func() OpenAIChatCompletionTranslator { return NewChatCompletionOpenAIToGCPAnthropicTranslator("", "") },
		"AWSAnthropic": func() OpenAIChatCompletionTranslator { return NewChatCompletionOpenAIToAWSAnthropicTranslator("", "") },
	}
	for name, newTranslator := range translators {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				enabled   bool
				req       *openai.ChatCompletionRequest
				wantPaths []string
			}{
				{
					name:    "enabled",
					enabled: true,
					req:     newRequest("You are a helpful assistant."),
					// The tool message becomes the last user turn, a tool_result block.
					wantPaths: []string{
						"messages.2.content.0.cache_control",
						"system.0.cache_control",
						"tools.1.cache_control",
					},
				},
				{
					name:      "disabled",
					enabled:   false,
					req:       newRequest("You are a helpful assistant."),
					wantPaths: nil,
				},
				{
					name:      "enabled, request already has a breakpoint",
					enabled:   true,
					req:       newRequest(cachedSystem),
					wantPaths: []string{"system.0.cache_control"},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tr := newTranslator()
					setter, ok := tr.(CachePointSetter)
					require.True(t, ok)
					setter.SetAutomaticCachePoints(tc.enabled)
					_, body, err := tr.RequestBody(nil, tc.req, false)
					require.NoError(t, err)
					require.Equal(t, tc.wantPaths, cacheControlPaths(body))
				})
			}
		})
	}
}

// cacheControlPaths returns the sorted gjson paths of every cache_control field in body.
func cacheControlPaths(body []byte) []string {
	var paths []string
	var walk func(prefix string, v gjson.Result)
	walk = func(prefix string, v gjson.Result) {
		i := 0
		v.ForEach(func(key, val gjson.Result) bool {
			k := key.String()
			if v.IsArray() {
				k = strconv.Itoa(i)
				i++
			}
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if k == "cache_control" {
				paths = append(paths, p)
			} else if val.IsObject() || val.IsArray() {
				walk(p, val)
			}
			return true
		})
	}
	walk("", gjson.ParseBytes(body))
	sort.Strings(paths)
	return paths
}
