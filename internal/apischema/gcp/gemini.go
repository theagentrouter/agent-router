// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import (
	"cmp"
	"errors"
	"net/url"
	"strings"

	"google.golang.org/genai"
)

const (
	geminiGenerateContent       = "generateContent"
	geminiStreamGenerateContent = "streamGenerateContent"
)

// GeminiGenerateContentPath captures the routing fields of a Gemini Developer API request.
type GeminiGenerateContentPath struct {
	Model  string
	method string
	alt    string
}

// ParseGeminiGenerateContentPath reads a model and action below the registered models prefix.
func ParseGeminiGenerateContentPath(prefix, requestPath string) (GeminiGenerateContentPath, error) {
	resource, rawQuery, _ := strings.Cut(requestPath, "?")
	suffix, ok := strings.CutPrefix(resource, prefix+"/")
	model, method, _ := strings.Cut(suffix, ":")
	if !ok || !IsGeminiModelID(model) {
		return GeminiGenerateContentPath{}, errors.New("invalid Gemini model path")
	}
	if method != geminiGenerateContent && method != geminiStreamGenerateContent {
		return GeminiGenerateContentPath{}, errors.New("unsupported Gemini model action")
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return GeminiGenerateContentPath{}, errors.New("invalid Gemini query parameters")
	}
	// Other system parameters can carry credentials, change the billing project,
	// or hide the usage metadata. The gateway only forwards the response format.
	alt := cmp.Or(query.Get("alt"), query.Get("$alt"))
	if alt != "" && alt != "json" && alt != "sse" {
		return GeminiGenerateContentPath{}, errors.New("unsupported Gemini response format")
	}
	return GeminiGenerateContentPath{Model: model, method: method, alt: alt}, nil
}

// IsGeminiModelID reports whether model can be forwarded as a single resource segment.
func IsGeminiModelID(model string) bool {
	return model != "" && model != "." && model != ".." && !strings.ContainsAny(model, "/%\\:#? \t\r\n")
}

// Streaming reports whether the request uses the streaming action.
func (p GeminiGenerateContentPath) Streaming() bool {
	return p.method == geminiStreamGenerateContent
}

// Path builds a path below prefix with the original action and safe query parameters.
func (p GeminiGenerateContentPath) Path(prefix, model string) string {
	path := prefix + "/" + model + ":" + p.method
	if p.alt != "" {
		path += "?alt=" + p.alt
	}
	return path
}

// GeminiGenerateContentRequest contains the fields the gateway needs from a native request.
// The request body itself is opaque and is forwarded unchanged.
type GeminiGenerateContentRequest struct {
	Model string `json:"-"`
}

// GeminiGenerateContentResponse contains the metadata read from a Gemini response or stream
// chunk. Leaving content opaque preserves fields the gateway does not model, such as signatures.
type GeminiGenerateContentResponse struct {
	ResponseID    string                                      `json:"responseId"`
	ModelVersion  string                                      `json:"modelVersion"`
	UsageMetadata *genai.GenerateContentResponseUsageMetadata `json:"usageMetadata"`
	Error         *struct {
		Code int `json:"code"`
	} `json:"error,omitempty"`
}

// MergeGeminiUsage updates the reported counts with the metadata from the latest chunk.
// A chunk can omit previously reported prompt, cache or output counts.
func MergeGeminiUsage(previous, current *genai.GenerateContentResponseUsageMetadata) *genai.GenerateContentResponseUsageMetadata {
	if current == nil {
		return previous
	}
	if previous == nil {
		return current
	}
	merged := *current
	merged.PromptTokenCount = cmp.Or(current.PromptTokenCount, previous.PromptTokenCount)
	merged.ToolUsePromptTokenCount = cmp.Or(current.ToolUsePromptTokenCount, previous.ToolUsePromptTokenCount)
	merged.CachedContentTokenCount = cmp.Or(current.CachedContentTokenCount, previous.CachedContentTokenCount)
	merged.CandidatesTokenCount = cmp.Or(current.CandidatesTokenCount, previous.CandidatesTokenCount)
	merged.ThoughtsTokenCount = cmp.Or(current.ThoughtsTokenCount, previous.ThoughtsTokenCount)
	merged.TotalTokenCount = cmp.Or(current.TotalTokenCount, previous.TotalTokenCount)
	return &merged
}

// GeminiUsageCounts returns the input, output and total token counts for native generation.
func GeminiUsageCounts(m *genai.GenerateContentResponseUsageMetadata) (input, output, total int) {
	input = int(m.PromptTokenCount) + int(m.ToolUsePromptTokenCount)
	output = int(m.CandidatesTokenCount) + int(m.ThoughtsTokenCount)
	total = max(int(m.TotalTokenCount), input+output)
	return
}
