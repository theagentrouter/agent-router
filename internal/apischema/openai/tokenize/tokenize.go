// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package tokenize contains Tokenize API schema definitions.
package tokenize

import (
	"errors"

	"github.com/tidwall/gjson"
	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// Different than vLLM's definition, "model" field should be a required field for both CompletionRequest and ChatRequest in a gateway.

// CompletionRequest represents a request to tokenize a completion prompt.
type CompletionRequest struct {
	// Model is the model to use for tokenization.
	Model string `json:"model"`
	// Prompt is the text prompt to tokenize.
	Prompt string `json:"prompt"`

	// AddSpecialTokens indicates if special tokens (e.g. BOS) will be added to the prompt.
	// Default is true: a completion request tokenizes a raw prompt string, so the model's
	// special tokens are normally wanted. This mirrors vLLM's /tokenize default and is
	// deliberately the opposite of ChatRequest.AddSpecialTokens (where the chat template
	// already inserts them).
	AddSpecialTokens *bool `json:"add_special_tokens,omitempty"`
	// ReturnTokenStrs indicates if token strings corresponding to the token ids should be returned.
	// Default is false.
	ReturnTokenStrs *bool `json:"return_token_strs,omitempty"`
}

// ChatRequest represents a request to tokenize chat messages.
type ChatRequest struct {
	// Model is the model to use for tokenization.
	Model string `json:"model"`
	// Messages are the chat messages to tokenize.
	Messages []openai.ChatCompletionMessageParamUnion `json:"messages"`

	// AddGenerationPrompt indicates if the generation prompt will be added to the chat template.
	// This is a parameter used by chat template in tokenizer config of the model.
	// Default is true.
	AddGenerationPrompt *bool `json:"add_generation_prompt,omitempty"`
	// ReturnTokenStrs indicates if token strings corresponding to the token ids should be returned.
	// Default is false.
	ReturnTokenStrs *bool `json:"return_token_strs,omitempty"`
	// ContinueFinalMessage indicates if the chat will be formatted so that the final
	// message in the chat is open-ended, without any EOS tokens. The model will continue
	// this message rather than starting a new one. This allows you to "prefill" part of
	// the model's response for it. Cannot be used at the same time as AddGenerationPrompt.
	// Default is false.
	ContinueFinalMessage bool `json:"continue_final_message,omitzero"`
	// AddSpecialTokens indicates if special tokens (e.g. BOS) will be added to the prompt
	// on top of what is added by the chat template. For most models, the chat template
	// takes care of adding the special tokens so this should be set to false.
	// Default is false (deliberately the opposite of CompletionRequest.AddSpecialTokens)
	// to avoid doubling the special tokens the chat template already inserts. This mirrors
	// vLLM's /tokenize default.
	AddSpecialTokens bool `json:"add_special_tokens,omitzero"`
	// ChatTemplate is a Jinja template to use for this conversion.
	// As of transformers v4.44, default chat template is no longer allowed,
	// so you must provide a chat template if the tokenizer does not define one.
	ChatTemplate *string `json:"chat_template,omitempty"`
	// ChatTemplateKwargs are additional keyword args to pass to the template renderer.
	// Will be accessible by the chat template.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	// MmProcessorKwargs are additional kwargs to pass to the HF processor.
	MmProcessorKwargs map[string]any `json:"mm_processor_kwargs,omitempty"`
	// Tools is a list of tools the model may call.
	Tools []openai.Tool `json:"tools,omitempty"`

	// GCP VertexAI specific
	MediaResolution genai.MediaResolution `json:"media_resolution,omitempty"`
}

// Validate checks that the request is valid.
func (r *ChatRequest) Validate() error {
	// AddGenerationPrompt defaults to true when unset (nil), matching the vLLM
	// chat template contract, so treat nil as true for the conflict check.
	addGenerationPrompt := true
	if r.AddGenerationPrompt != nil {
		addGenerationPrompt = *r.AddGenerationPrompt
	}
	if r.ContinueFinalMessage && addGenerationPrompt {
		return errors.New("cannot set both continue_final_message and add_generation_prompt to true")
	}
	return nil
}

// RequestUnion represents a union of tokenize request types.
// This allows the endpoint to handle both completion and chat tokenize requests.
type RequestUnion struct {
	// OfCompletion is set when this is a completion tokenize request
	*CompletionRequest `json:",omitzero,inline"`
	// OfChat is set when this is a chat tokenize request
	*ChatRequest `json:",omitzero,inline"`
}

// UnmarshalJSON selects the request type from the payload: a "messages" field
// means a chat request, "prompt" (without "messages") means a completion
// request, and having both is invalid.
func (r *RequestUnion) UnmarshalJSON(data []byte) error {
	hasMessages := gjson.GetBytes(data, "messages").Exists()
	hasPrompt := gjson.GetBytes(data, "prompt").Exists()
	if hasMessages && hasPrompt {
		return errors.New("tokenize request must have either 'prompt' or 'messages', not both")
	}
	if hasMessages {
		var chat ChatRequest
		if err := json.Unmarshal(data, &chat); err != nil {
			return err
		}
		r.ChatRequest = &chat
		return nil
	}
	var completion CompletionRequest
	if err := json.Unmarshal(data, &completion); err != nil {
		return err
	}
	r.CompletionRequest = &completion
	return nil
}

// Validate checks that exactly one request type is set in the union.
func (r *RequestUnion) Validate() error {
	if r.CompletionRequest != nil && r.ChatRequest != nil {
		return errors.New("only one request type can be set")
	}
	if r.CompletionRequest == nil && r.ChatRequest == nil {
		return errors.New("one request type must be set")
	}
	if (r.CompletionRequest != nil && r.CompletionRequest.Model == "") ||
		(r.ChatRequest != nil && r.ChatRequest.Model == "") {
		return errors.New("model is required")
	}
	// Delegate to the active request's own validation so the union is
	// self-validating for every caller, not just the ParseBody path.
	if r.ChatRequest != nil {
		if err := r.ChatRequest.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Response represents the response from a tokenize request.
type Response struct {
	// Count is the number of tokens.
	Count int `json:"count"`
	// MaxModelLen is the maximum model length.
	MaxModelLen int `json:"max_model_len"`
	// Tokens are the token IDs.
	Tokens []int `json:"tokens"`
	// TokenStrs are the token strings, if requested.
	TokenStrs []string `json:"token_strs,omitempty"`
}
