// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"io"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

func TestNewRecordersFromEnv(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		env  string
	}{
		{name: "unset selects the default", set: false},
		{name: "empty selects the default", set: true, env: ""},
		{name: "openinference", set: true, env: "openinference"},
		{name: "gen_ai", set: true, env: "gen_ai"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			internaltesting.ClearTestEnv(t)
			if tc.set {
				t.Setenv(EnvTracingSemConv, tc.env)
			}

			recorders, err := newRecordersFromEnv()
			require.NoError(t, err)
			requireAllRecordersSet(t, &recorders)
		})
	}
}

// TestNewRecordersFromEnv_default pins that an unset variable resolves to the
// same recorders as an explicit "openinference", so adding a convention cannot
// silently change the default.
func TestNewRecordersFromEnv_default(t *testing.T) {
	internaltesting.ClearTestEnv(t)

	fromUnset, err := newRecordersFromEnv()
	require.NoError(t, err)

	t.Setenv(EnvTracingSemConv, "openinference")
	fromExplicit, err := newRecordersFromEnv()
	require.NoError(t, err)

	require.Equal(t, "openinference", semConvs[0].name, "the first entry is the default")
	require.Equal(t, fromExplicit.unboundedAttributeCount, fromUnset.unboundedAttributeCount)
	require.IsType(t, fromExplicit.chatCompletion, fromUnset.chatCompletion)
}

func TestNewRecordersFromEnv_invalid(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{name: "unknown convention", env: "openlit"},
		// Matching is exact: fuzzy matching a security-relevant config knob is
		// how a typo silently selects the wrong convention.
		{name: "wrong case", env: "OpenInference"},
		{name: "trailing space", env: "openinference "},
		{name: "gen_ai wrong case", env: "GEN_AI"},
		{name: "genai without separator", env: "genai"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			internaltesting.ClearTestEnv(t)
			t.Setenv(EnvTracingSemConv, tc.env)

			_, err := newRecordersFromEnv()
			require.Error(t, err)
			require.ErrorContains(t, err, EnvTracingSemConv)
			require.ErrorContains(t, err, tc.env)
			// The message must enumerate every valid name so the fix is obvious.
			for _, sc := range semConvs {
				require.ErrorContains(t, err, sc.name)
			}
		})
	}
}

// TestSemConvs_complete walks the registry generically so that adding a new
// endpoint to recorderSet, or a new convention, fails here rather than panicking
// on a nil recorder at request time.
func TestSemConvs_complete(t *testing.T) {
	require.NotEmpty(t, semConvs)

	seen := make(map[string]struct{}, len(semConvs))
	for _, sc := range semConvs {
		t.Run(sc.name, func(t *testing.T) {
			require.NotEmpty(t, sc.name)
			require.NotContains(t, seen, sc.name, "duplicate convention name")
			seen[sc.name] = struct{}{}
			require.NotNil(t, sc.newRecorders)

			internaltesting.ClearTestEnv(t)
			recorders := sc.newRecorders()
			requireAllRecordersSet(t, &recorders)
		})
	}
}

// TestNewTracingFromEnv_invalidSemConv pins that an invalid convention fails
// startup rather than silently falling back.
func TestNewTracingFromEnv_invalidSemConv(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "console")
	t.Setenv(EnvTracingSemConv, "nope")

	_, err := NewTracingFromEnv(t.Context(), io.Discard, nil)
	require.ErrorContains(t, err, EnvTracingSemConv)
}

// TestNewTracingFromEnv_semConvNotReadWhenDisabled pins that the convention is
// only resolved once tracing is actually enabled, so an invalid value cannot
// break a gateway that has tracing turned off.
func TestNewTracingFromEnv_semConvNotReadWhenDisabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "sdk disabled", env: map[string]string{"OTEL_SDK_DISABLED": "true"}},
		{name: "exporter none", env: map[string]string{"OTEL_TRACES_EXPORTER": "none"}},
		{name: "no exporter configured", env: map[string]string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			internaltesting.ClearTestEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			t.Setenv(EnvTracingSemConv, "nope")

			tracing, err := NewTracingFromEnv(t.Context(), io.Discard, nil)
			require.NoError(t, err)
			require.Equal(t, tracingapi.NoopTracing{}, tracing)
		})
	}
}

func requireAllRecordersSet(t *testing.T, r *recorderSet) {
	t.Helper()
	require.NotNil(t, r.chatCompletion, "chatCompletion")
	require.NotNil(t, r.completion, "completion")
	require.NotNil(t, r.embeddings, "embeddings")
	require.NotNil(t, r.imageGeneration, "imageGeneration")
	require.NotNil(t, r.responses, "responses")
	require.NotNil(t, r.speech, "speech")
	require.NotNil(t, r.transcription, "transcription")
	require.NotNil(t, r.translation, "translation")
	require.NotNil(t, r.rerank, "rerank")
	require.NotNil(t, r.message, "message")
	require.NotNil(t, r.tokenize, "tokenize")
	require.NotNil(t, r.responsesInputTokens, "responsesInputTokens")
	require.NotNil(t, r.countTokens, "countTokens")

	// The MCP vocabulary is a struct of function fields rather than a recorder
	// interface, so "supplied" means every field is populated. A nil field would
	// panic on the first MCP request under that convention.
	require.NotEmpty(t, r.mcp.name, "mcp.name")
	require.NotNil(t, r.mcp.spanName, "mcp.spanName")
	require.NotNil(t, r.mcp.requestAttributes, "mcp.requestAttributes")
	require.NotNil(t, r.mcp.routeToBackend, "mcp.routeToBackend")
	require.NotNil(t, r.mcp.clientSession, "mcp.clientSession")
	require.NotNil(t, r.mcp.event, "mcp.event")
	require.NotNil(t, r.mcp.listResult, "mcp.listResult")
	require.NotNil(t, r.mcp.toolCallResult, "mcp.toolCallResult")
	require.NotNil(t, r.mcp.requestError, "mcp.requestError")
}

// TestNewRecordersFromEnv_mcpVocabulary pins which MCP vocabulary each
// convention selects. The default must stay on the legacy one: its attribute
// keys and span names are what existing dashboards query, and changing them
// without the user opting in is a breaking change.
func TestNewRecordersFromEnv_mcpVocabulary(t *testing.T) {
	tests := []struct {
		name string
		set  bool
		env  string
		want string
	}{
		{name: "unset keeps the legacy vocabulary", set: false, want: "openinference"},
		{name: "empty keeps the legacy vocabulary", set: true, env: "", want: "openinference"},
		{name: "openinference keeps the legacy vocabulary", set: true, env: "openinference", want: "openinference"},
		{name: "gen_ai opts into the OTel vocabulary", set: true, env: "gen_ai", want: "gen_ai"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			internaltesting.ClearTestEnv(t)
			if tc.set {
				t.Setenv(EnvTracingSemConv, tc.env)
			}

			recorders, err := newRecordersFromEnv()
			require.NoError(t, err)
			require.Equal(t, tc.want, recorders.mcp.name)
		})
	}
}

// TestMCPVocabulary_contentCaptureFollowsConvention pins that MCP tool call
// content is gated by the convention's own opt-in. The legacy vocabulary never
// recorded content, so it must not start doing so because a GenAI variable is
// set for the LLM endpoints.
func TestMCPVocabulary_contentCaptureFollowsConvention(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "true")

	recorders, err := newRecordersFromEnv()
	require.NoError(t, err)
	require.Equal(t, "openinference", recorders.mcp.name)

	span, exported := newTestLegacyMCPSpan(t, "tools/call", &mcp.CallToolParams{
		Name:      "fake-tool",
		Arguments: map[string]any{"service": "payment-api"},
	})
	span.RecordToolCallResult([]byte(`{"status":200}`))
	span.EndSpan()

	for _, a := range exported().Attributes {
		require.NotEqual(t, "gen_ai.tool.call.arguments", string(a.Key))
		require.NotEqual(t, "gen_ai.tool.call.result", string(a.Key))
	}
}
