// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// CompletionCassettes returns a slice of all cassettes for the /completions endpoint.
func CompletionCassettes() []Cassette {
	return cassettes(completionRequests)
}

const (
	// fibPrompt represents a common code completion scenario for LoRA-tuned
	// code models like CodeLlama or Starcoder that are frequently deployed via
	// vLLM/llama.cpp
	fibPrompt = `def fib(n):
    if n <= 1:
        return n
    else:
        return fib(n-1) + fib(n-2)`
	// fibPromptPartial mimics mid-edit autocomplete.
	fibPromptPartial = `def fib(n):
    if n <= 1:
        return n
    else:`
)

var (
	// fibPromptTokens is fibPrompt in cl100k_base tokens.
	fibPromptTokens = []int64{755, 16178, 1471, 997, 262, 422, 308, 2717, 220, 16, 512, 286, 471, 308, 198, 262, 775, 512, 286, 471, 16178, 1471, 12, 16, 8, 489, 16178, 1471, 12, 17, 8}
	// fibPromptPartialTokens is fibPromptPartial in cl100k_base tokens.
	fibPromptPartialTokens = []int64{755, 16178, 1471, 997, 262, 422, 308, 2717, 220, 16, 512, 286, 471, 308, 198, 262, 775, 25}
)

// completionRequests contains the actual request body for each completion cassette.
var completionRequests = map[Cassette]*openai.CompletionRequest{
	CassetteCompletionBasic: cassetteCompletionBasic,
	CassetteCompletionToken: {
		Model:       openai.ModelBabbage002,
		Prompt:      openai.PromptUnion{Value: fibPromptTokens},
		MaxTokens:   new(25),
		Temperature: new(0.5),
		TopP:        new(0.9),
	},
	CassetteCompletionStreaming:      completionWithStream(cassetteCompletionBasic),
	CassetteCompletionStreamingUsage: completionWithStreamUsage(cassetteCompletionBasic),
	CassetteCompletionTextBatch: {
		Model:       openai.ModelBabbage002,
		Prompt:      openai.PromptUnion{Value: []string{fibPrompt, fibPromptPartial}},
		MaxTokens:   new(25),
		Temperature: new(0.5),
		TopP:        new(0.9),
		N:           new(2),           // Multiple completions for user choice in IDE
		Stop:        []string{"\n\n"}, // Stop at function boundaries for clean completion
	},
	CassetteCompletionTokenBatch: {
		Model:       openai.ModelBabbage002,
		Prompt:      openai.PromptUnion{Value: [][]int64{fibPromptTokens, fibPromptPartialTokens}},
		MaxTokens:   new(25),
		Temperature: new(0.5),
		TopP:        new(0.9),
		N:           new(2),
		Stop:        []string{"\n\n"},
	},
	CassetteCompletionSuffix: {
		Model:       openai.ModelGPT35TurboInstruct, // supports suffix
		Prompt:      openai.PromptUnion{Value: fibPromptPartial},
		MaxTokens:   new(25),
		Temperature: new(0.5),
		TopP:        new(0.9),
		N:           new(2),
		Suffix:      "\nprint(fib(10))",         // Infilling pattern used in code LoRA training
		Logprobs:    new(3),                     // Confidence analysis for LoRA model comparison
		Stop:        []string{"def ", "class "}, // Prevent generating additional functions
	},

	CassetteCompletionBadRequest: {
		Model:       openai.ModelBabbage002,
		Prompt:      openai.PromptUnion{Value: ""},
		MaxTokens:   new(-1),  // Invalid negative integer
		Temperature: new(3.0), // Invalid float >2.0
	},

	CassetteCompletionUnknownModel: {
		Model:  openai.ModelBabbage002 + "-wrong",
		Prompt: openai.PromptUnion{Value: fibPrompt},
	},
}

var cassetteCompletionBasic = &openai.CompletionRequest{
	Model:       openai.ModelBabbage002,               // Base model for LoRA adapter testing
	Prompt:      openai.PromptUnion{Value: fibPrompt}, // Standard code completion task
	MaxTokens:   new(25),                              // Adequate for function body completion
	Temperature: new(0.4),                             // Lower temperature for focused code generation
	TopP:        new(0.9),                             // Nucleus sampling for quality code output
}

func completionWithStream(req *openai.CompletionRequest) *openai.CompletionRequest {
	if req == nil {
		return nil
	}
	clone := *req // shallow copy.
	clone.Stream = true
	return &clone
}

func completionWithStreamUsage(req *openai.CompletionRequest) *openai.CompletionRequest {
	clone := completionWithStream(req)
	clone.StreamOptions = &openai.StreamOptions{IncludeUsage: true}
	return clone
}

//go:fix inline
