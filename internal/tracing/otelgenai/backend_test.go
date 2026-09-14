// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package otelgenai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// TestProviderBySchema_matchesFilterAPI pins the schema keys against the real
// constants. The map is keyed by string so this package stays free of a
// filterapi dependency, which means a typo would silently fall through to the
// backend name instead of failing. This test is what catches that.
func TestProviderBySchema_matchesFilterAPI(t *testing.T) {
	tests := []struct {
		schema   filterapi.APISchemaName
		expected Provider
	}{
		{schema: filterapi.APISchemaOpenAI, expected: ProviderOpenAI},
		{schema: filterapi.APISchemaAzureOpenAI, expected: ProviderAzureOpenAI},
		{schema: filterapi.APISchemaAWSBedrock, expected: ProviderAWSBedrock},
		{schema: filterapi.APISchemaAWSAnthropic, expected: ProviderAWSAnthropic},
		{schema: filterapi.APISchemaGCPVertexAI, expected: ProviderGCPVertexAI},
		{schema: filterapi.APISchemaGCPAnthropic, expected: ProviderGCPAnthropic},
		{schema: filterapi.APISchemaAnthropic, expected: ProviderAnthropic},
		{schema: filterapi.APISchemaCohere, expected: ProviderCohere},
	}

	for _, tc := range tests {
		t.Run(string(tc.schema), func(t *testing.T) {
			got, ok := providerBySchema[string(tc.schema)]
			require.True(t, ok, "schema %q is not mapped; a typo here silently degrades to the backend name", tc.schema)
			require.Equal(t, tc.expected, got)
		})
	}

	require.Len(t, providerBySchema, len(tests),
		"a schema was added to or removed from the map without updating this test")
}

func TestProviderForSchema(t *testing.T) {
	tests := []struct {
		name        string
		schema      string
		backendName string
		expected    string
	}{
		{name: "known schema", schema: "OpenAI", backendName: "my-backend", expected: "openai"},
		{name: "known schema ignores backend name", schema: "Cohere", backendName: "my-backend", expected: "cohere"},
		// Custom values are permitted by the conventions, so an unknown schema
		// reports the backend name rather than dropping the required attribute.
		{name: "unknown schema falls back", schema: "SomeVendor", backendName: "my-backend", expected: "my-backend"},
		{name: "empty schema falls back", schema: "", backendName: "my-backend", expected: "my-backend"},
		{name: "wrong case falls back", schema: "openai", backendName: "my-backend", expected: "my-backend"},
		{name: "both empty yields empty", schema: "", backendName: "", expected: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, ProviderForSchema(tc.schema, tc.backendName))
		})
	}
}

func TestRecorder_RecordBackend(t *testing.T) {
	tests := []struct {
		name     string
		backend  tracingapi.Backend
		expected []attribute.KeyValue
	}{
		{
			name:    "known schema",
			backend: tracingapi.Backend{Schema: "OpenAI", Name: "b"},
			expected: []attribute.KeyValue{
				attribute.String(ProviderName, "openai"),
			},
		},
		{
			name:    "another known schema",
			backend: tracingapi.Backend{Schema: "AWSBedrock", Name: "b"},
			expected: []attribute.KeyValue{
				attribute.String(ProviderName, "aws.bedrock"),
			},
		},
		{
			name:    "unknown schema reports backend name",
			backend: tracingapi.Backend{Schema: "SomeVendor", Name: "my-backend"},
			expected: []attribute.KeyValue{
				attribute.String(ProviderName, "my-backend"),
			},
		},
		{
			name:     "empty backend records nothing",
			backend:  tracingapi.Backend{},
			expected: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewChatCompletionRecorder(NewConfig())
			br, ok := r.(tracingapi.BackendRecorder)
			require.True(t, ok, "GenAI recorders must record the required gen_ai.provider.name")

			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				br.RecordBackend(span, tc.backend)
				return false
			})

			testotel.RequireAttributesEqual(t, tc.expected, span.Attributes)
		})
	}
}

// TestRecorder_RecordBackend_allEndpoints pins that every endpoint records the
// provider, since it is a required attribute for all of them.
func TestRecorder_RecordBackend_allEndpoints(t *testing.T) {
	cfg := NewConfig()
	recorders := map[string]any{
		"chatCompletion":  NewChatCompletionRecorder(cfg),
		"completion":      NewCompletionRecorder(cfg),
		"embeddings":      NewEmbeddingsRecorder(cfg),
		"imageGeneration": NewImageGenerationRecorder(cfg),
		"responses":       NewResponsesRecorder(cfg),
		"speech":          NewSpeechRecorder(cfg),
		"transcription":   NewTranscriptionRecorder(cfg),
		"translation":     NewTranslationRecorder(cfg),
		"rerank":          NewRerankRecorder(cfg),
		"message":         NewMessageRecorder(cfg),
		"tokenize":        NewTokenizeRecorder(cfg),
	}

	for name, r := range recorders {
		t.Run(name, func(t *testing.T) {
			br, ok := r.(tracingapi.BackendRecorder)
			require.True(t, ok)

			span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
				br.RecordBackend(span, tracingapi.Backend{Schema: "OpenAI", Name: "b"})
				return false
			})

			testotel.RequireAttributesEqual(t, []attribute.KeyValue{
				attribute.String(ProviderName, "openai"),
			}, span.Attributes)
		})
	}
}

// TestOpenInferenceRecorder_doesNotRecordBackend documents the contract from the
// other side: recorders that do not implement BackendRecorder are simply
// skipped, which is how the default path stays byte-identical.
func TestRecorder_backendIsOptional(t *testing.T) {
	var notARecorder any = struct{}{}
	_, ok := notARecorder.(tracingapi.BackendRecorder)
	require.False(t, ok)

	// A GenAI recorder used as a plain span recorder still works.
	r := NewChatCompletionRecorder(NewConfig())
	span := testotel.RecordWithSpan(t, func(span oteltrace.Span) bool {
		r.RecordRequest(span, &openai.ChatCompletionRequest{Model: "m"}, nil)
		return false
	})
	require.NotEmpty(t, span.Attributes)
}
