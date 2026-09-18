// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tokenize

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestCompletionRequest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected CompletionRequest
		wantErr  bool
	}{
		{
			name:  "basic completion request",
			input: `{"prompt": "Hello world", "model": "gpt-4"}`,
			expected: CompletionRequest{
				Prompt: "Hello world",
				Model:  "gpt-4",
			},
		},
		{
			name:  "completion request with all fields",
			input: `{"prompt": "Hello", "model": "gpt-4", "add_special_tokens": true, "return_token_strs": true}`,
			expected: CompletionRequest{
				Prompt:           "Hello",
				Model:            "gpt-4",
				AddSpecialTokens: new(true),
				ReturnTokenStrs:  new(true),
			},
		},
		{
			name:  "completion request with defaults",
			input: `{"prompt": "Hello"}`,
			expected: CompletionRequest{
				Prompt: "Hello",
			},
		},
		{
			name:  "completion request missing prompt",
			input: `{"model": "gpt-4"}`,
			expected: CompletionRequest{
				Model:  "gpt-4",
				Prompt: "", // Empty prompt - should be validated elsewhere
			},
			wantErr: false, // JSON unmarshaling succeeds, validation should catch this
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req CompletionRequest
			err := json.Unmarshal([]byte(tt.input), &req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, req)

			// Test roundtrip marshaling
			data, err := json.Marshal(req)
			require.NoError(t, err)

			var req2 CompletionRequest
			err = json.Unmarshal(data, &req2)
			require.NoError(t, err)
			assert.Equal(t, req, req2)
		})
	}
}

func TestChatRequest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected ChatRequest
		wantErr  bool
	}{
		{
			name: "basic chat request",
			input: `{
				"messages": [{"role": "user", "content": "Hello"}],
				"model": "gpt-4"
			}`,
			expected: ChatRequest{
				Model:    "gpt-4",
				Messages: []openai.ChatCompletionMessageParamUnion{},
			},
			// Note: We can't easily create the expected Messages here due to the complex union type
		},
		{
			name: "chat request with all boolean fields",
			input: `{
				"messages": [{"role": "user", "content": "Hello"}],
				"add_generation_prompt": true,
				"continue_final_message": false,
				"add_special_tokens": true,
				"return_token_strs": true
			}`,
		},
		{
			name: "chat request with template",
			input: `{
				"messages": [{"role": "user", "content": "Hello"}],
				"chat_template": "{% for message in messages %}{{ message.content }}{% endfor %}",
				"chat_template_kwargs": {"key": "value"}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req ChatRequest
			err := json.Unmarshal([]byte(tt.input), &req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Test that marshaling works
			data, err := json.Marshal(req)
			require.NoError(t, err)
			assert.Contains(t, string(data), "messages")
		})
	}
}

func TestChatRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		request ChatRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid request",
			request: ChatRequest{
				AddGenerationPrompt:  new(true),
				ContinueFinalMessage: false,
			},
			wantErr: false,
		},
		{
			name: "conflicting flags",
			request: ChatRequest{
				AddGenerationPrompt:  new(true),
				ContinueFinalMessage: true,
			},
			wantErr: true,
			errMsg:  "cannot set both continue_final_message and add_generation_prompt to true",
		},
		{
			name: "continue final message only",
			request: ChatRequest{
				AddGenerationPrompt:  new(false),
				ContinueFinalMessage: true,
			},
			wantErr: false,
		},
		{
			// AddGenerationPrompt unset defaults to true, so it still conflicts.
			name: "continue final message with add_generation_prompt unset",
			request: ChatRequest{
				AddGenerationPrompt:  nil,
				ContinueFinalMessage: true,
			},
			wantErr: true,
			errMsg:  "cannot set both continue_final_message and add_generation_prompt to true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRequestUnion_JSON(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		isChat       bool
		isCompletion bool
	}{
		{
			name:         "completion request",
			input:        `{"prompt": "Hello world"}`,
			isCompletion: true,
		},
		{
			name:   "chat request",
			input:  `{"messages": [{"role": "user", "content": "Hello"}]}`,
			isChat: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var union RequestUnion
			err := json.Unmarshal([]byte(tt.input), &union)
			require.NoError(t, err)

			// Verify the discriminator selected the expected request type.
			if tt.isChat {
				require.NotNil(t, union.ChatRequest)
				require.Nil(t, union.CompletionRequest)
			}
			if tt.isCompletion {
				require.NotNil(t, union.CompletionRequest)
				require.Nil(t, union.ChatRequest)
			}

			// Test marshaling back
			data, err := json.Marshal(union)
			require.NoError(t, err)
			assert.NotEmpty(t, data)
		})
	}
}

func TestResponse_JSON(t *testing.T) {
	tests := []struct {
		name     string
		response Response
		wantJSON string
	}{
		{
			name: "basic response",
			response: Response{
				Count:       5,
				MaxModelLen: 4096,
				Tokens:      []int{1, 2, 3, 4, 5},
			},
			wantJSON: `{"count":5,"max_model_len":4096,"tokens":[1,2,3,4,5]}`,
		},
		{
			name: "response with token strings",
			response: Response{
				Count:       3,
				MaxModelLen: 2048,
				Tokens:      []int{1, 2, 3},
				TokenStrs:   []string{"Hello", " ", "world"},
			},
			wantJSON: `{"count":3,"max_model_len":2048,"tokens":[1,2,3],"token_strs":["Hello"," ","world"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.response)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(data))

			// Test roundtrip
			var response2 Response
			err = json.Unmarshal(data, &response2)
			require.NoError(t, err)
			assert.Equal(t, tt.response, response2)
		})
	}
}

func TestRequestUnion_HelperMethods(t *testing.T) {
	// This test highlights the need for helper methods
	t.Run("need helper methods to check union type", func(t *testing.T) {
		// Completion request
		union1 := RequestUnion{
			CompletionRequest: &CompletionRequest{
				Prompt: "Hello",
			},
		}

		// Chat request
		union2 := RequestUnion{
			ChatRequest: &ChatRequest{
				Messages: []openai.ChatCompletionMessageParamUnion{},
			},
		}

		// TODO: Add helper methods like:
		// union1.IsCompletion() bool
		// union1.IsChatCompletion() bool
		// union1.GetCompletion() *CompletionRequest
		// union1.GetChat() *ChatRequest

		// Current way to check (verbose)
		assert.NotNil(t, union1.CompletionRequest)
		assert.Nil(t, union1.ChatRequest)

		assert.Nil(t, union2.CompletionRequest)
		assert.NotNil(t, union2.ChatRequest)
	})
}

func TestRequestUnion_Validate(t *testing.T) {
	tests := []struct {
		name    string
		request RequestUnion
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid completion request",
			request: RequestUnion{
				CompletionRequest: &CompletionRequest{Model: "gpt-4", Prompt: "Hello"},
			},
			wantErr: false,
		},
		{
			name: "valid chat request",
			request: RequestUnion{
				ChatRequest: &ChatRequest{Model: "gpt-4"},
			},
			wantErr: false,
		},
		{
			name:    "no request type set",
			request: RequestUnion{},
			wantErr: true,
			errMsg:  "one request type must be set",
		},
		{
			name: "both request types set",
			request: RequestUnion{
				CompletionRequest: &CompletionRequest{Model: "gpt-4", Prompt: "Hello"},
				ChatRequest:       &ChatRequest{Model: "gpt-4"},
			},
			wantErr: true,
			errMsg:  "only one request type can be set",
		},
		{
			name: "completion request with empty model",
			request: RequestUnion{
				CompletionRequest: &CompletionRequest{Prompt: "Hello"},
			},
			wantErr: true,
			errMsg:  "model is required",
		},
		{
			name: "chat request with empty model",
			request: RequestUnion{
				ChatRequest: &ChatRequest{},
			},
			wantErr: true,
			errMsg:  "model is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.request.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// Helper functions
//
//go:fix inline
