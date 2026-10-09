// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	openaigo "github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/attribute"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
)

// The conventions record sampling parameters as discrete typed attributes,
// unlike OpenInference which packs them into one llm.invocation_parameters JSON
// blob. Absent parameters are omitted rather than recorded as zero, since zero
// is a meaningful value for temperature and penalties.
//
// params accumulates attributes so each endpoint's builder reads as a list of
// what that endpoint supports, and the encoding of any given parameter is
// defined exactly once.
type params struct {
	attrs []attribute.KeyValue
}

func (p *params) float64(key string, v *float64) {
	if v != nil {
		p.attrs = append(p.attrs, attribute.Float64(key, *v))
	}
}

func (p *params) float32(key string, v *float32) {
	if v != nil {
		p.attrs = append(p.attrs, attribute.Float64(key, float64(*v)))
	}
}

func (p *params) int(key string, v *int) {
	if v != nil {
		p.attrs = append(p.attrs, attribute.Int(key, *v))
	}
}

func (p *params) int64(key string, v *int64) {
	if v != nil {
		p.attrs = append(p.attrs, attribute.Int64(key, *v))
	}
}

func (p *params) stringSlice(key string, v []string) {
	if len(v) > 0 {
		p.attrs = append(p.attrs, attribute.StringSlice(key, v))
	}
}

// chatRequestAttrs builds the sampling parameters for an OpenAI chat request.
//
// MaxCompletionTokens supersedes the deprecated MaxTokens in the OpenAI API, so
// it wins when both are set.
func chatRequestAttrs(req *openai.ChatCompletionRequest) []attribute.KeyValue {
	var p params
	p.float64(RequestTemperature, req.Temperature)
	p.float64(RequestTopP, req.TopP)
	p.float32(RequestFrequencyPenalty, req.FrequencyPenalty)
	p.float32(RequestPresencePenalty, req.PresencePenalty)
	p.int(RequestSeed, req.Seed)
	p.int(RequestChoiceCount, req.N)

	maxTokens := req.MaxCompletionTokens
	if maxTokens == nil {
		maxTokens = req.MaxTokens
	}
	p.int64(RequestMaxTokens, maxTokens)

	p.stringSlice(RequestStopSequences, chatStopSequences(req.Stop))
	return p.attrs
}

// completionRequestAttrs builds the sampling parameters for a legacy completion
// request. The field types differ from chat completions, which is why this is
// not shared.
func completionRequestAttrs(req *openai.CompletionRequest) []attribute.KeyValue {
	var p params
	p.float64(RequestTemperature, req.Temperature)
	p.float64(RequestTopP, req.TopP)
	p.float64(RequestFrequencyPenalty, req.FrequencyPenalty)
	p.float64(RequestPresencePenalty, req.PresencePenalty)
	p.int64(RequestSeed, req.Seed)
	p.int(RequestChoiceCount, req.N)
	if req.MaxTokens != nil {
		p.attrs = append(p.attrs, attribute.Int(RequestMaxTokens, *req.MaxTokens))
	}
	p.stringSlice(RequestStopSequences, anyStopSequences(req.Stop))
	return p.attrs
}

// embeddingsRequestAttrs records the encoding format, which is the only
// sampling-like parameter the conventions define for embeddings.
func embeddingsRequestAttrs(req *openai.EmbeddingRequest) []attribute.KeyValue {
	var p params
	if req.EncodingFormat != nil && *req.EncodingFormat != "" {
		p.stringSlice(RequestEncodingFormats, []string{*req.EncodingFormat})
	}
	return p.attrs
}

// chatStopSequences normalizes the stop union into a list. The conventions
// define stop sequences as an array even when the caller supplied a single
// string.
func chatStopSequences(stop openaigo.ChatCompletionNewParamsStopUnion) []string {
	if s := stop.OfString; s.Valid() {
		return []string{s.Value}
	}
	return stop.OfStringArray
}

// anyStopSequences normalizes the loosely typed stop field used by the legacy
// completions API.
func anyStopSequences(stop any) []string {
	switch s := stop.(type) {
	case string:
		if s == "" {
			return nil
		}
		return []string{s}
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, v := range s {
			if str, ok := v.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
