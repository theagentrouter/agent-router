// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package vcr

import (
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/tests/internal/testopenai"
)

// These tests exercise the OpenTelemetry GenAI semantic conventions end to end
// through a real ExtProc and Envoy, selected by AI_GATEWAY_TRACING_SEMCONV.
//
// Unlike the OpenInference tests, they assert on attributes directly rather than
// diffing against golden spans. The OpenInference goldens are recorded from the
// reference Python instrumentation, so a diff there is a conformance claim
// against an external implementation. No comparable reference exists for the
// GenAI conventions across these endpoints, so a golden would only assert that
// our output matches our own previous output. Explicit assertions state what
// the spec requires and are reviewable on their own.

const genAISemConvEnv = "AI_GATEWAY_TRACING_SEMCONV=gen_ai"

func TestOtelGenAIChatCompletions_span(t *testing.T) {
	env := setupOtelTestEnvironment(t, genAISemConvEnv)

	req, err := testopenai.NewRequest(t.Context(),
		fmt.Sprintf("http://localhost:%d", env.listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	was5xx := false
	failIf5xx(t, resp, &was5xx)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	// The conventions name inference spans "{operation} {model}" and mark them
	// CLIENT, unlike OpenInference's fixed "ChatCompletion" INTERNAL span.
	require.Equal(t, tracev1.Span_SPAN_KIND_CLIENT, span.Kind)
	require.Equal(t, "chat "+requireStringAttr(t, span, "gen_ai.request.model"), span.Name)

	// gen_ai.provider.name is required, and is only knowable after routing
	// resolves a backend.
	require.Equal(t, "openai", requireStringAttr(t, span, "gen_ai.provider.name"))
	require.Equal(t, "chat", requireStringAttr(t, span, "gen_ai.operation.name"))
	require.NotEmpty(t, requireStringAttr(t, span, "gen_ai.response.model"))

	// Usage is metadata, so it is recorded even with content capture off.
	require.Positive(t, requireIntAttr(t, span, "gen_ai.usage.input_tokens"))
	require.Positive(t, requireIntAttr(t, span, "gen_ai.usage.output_tokens"))

	// No OpenInference attribute may appear when this convention is selected.
	for _, attr := range span.Attributes {
		require.NotContains(t, attr.Key, "llm.", "OpenInference attribute leaked into a gen_ai span")
		require.NotContains(t, attr.Key, "openinference.")
	}

	_ = env.collector.DrainMetrics()
}

// TestOtelGenAIChatCompletions_noContentByDefault pins that prompts and
// completions are absent unless the operator opts in, which is the default the
// GenAI conventions require and the opposite of OpenInference's.
func TestOtelGenAIChatCompletions_noContentByDefault(t *testing.T) {
	env := setupOtelTestEnvironment(t, genAISemConvEnv)

	req, err := testopenai.NewRequest(t.Context(),
		fmt.Sprintf("http://localhost:%d", env.listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	was5xx := false
	failIf5xx(t, resp, &was5xx)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	for _, attr := range span.Attributes {
		require.NotEqual(t, "gen_ai.input.messages", attr.Key)
		require.NotEqual(t, "gen_ai.output.messages", attr.Key)
	}

	_ = env.collector.DrainMetrics()
}

// TestOtelGenAIChatCompletions_captureContent pins the opt-in path.
func TestOtelGenAIChatCompletions_captureContent(t *testing.T) {
	env := setupOtelTestEnvironment(t, genAISemConvEnv,
		"OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=true")

	req, err := testopenai.NewRequest(t.Context(),
		fmt.Sprintf("http://localhost:%d", env.listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	was5xx := false
	failIf5xx(t, resp, &was5xx)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	// Messages are a single JSON attribute per direction rather than one
	// attribute per message.
	require.NotEmpty(t, requireStringAttr(t, span, "gen_ai.input.messages"))
	require.NotEmpty(t, requireStringAttr(t, span, "gen_ai.output.messages"))

	_ = env.collector.DrainMetrics()
}

// TestOtelDefaultSemConvIsOpenInference pins that the new variable is opt-in:
// with it unset, spans keep the OpenInference shape.
func TestOtelDefaultSemConvIsOpenInference(t *testing.T) {
	env := setupOtelTestEnvironment(t)

	req, err := testopenai.NewRequest(t.Context(),
		fmt.Sprintf("http://localhost:%d", env.listenerPort), testopenai.CassetteChatBasic)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	was5xx := false
	failIf5xx(t, resp, &was5xx)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	span := env.collector.TakeSpan()
	require.NotNil(t, span)

	require.Equal(t, "ChatCompletion", span.Name)
	require.Equal(t, tracev1.Span_SPAN_KIND_INTERNAL, span.Kind)
	for _, attr := range span.Attributes {
		require.NotContains(t, attr.Key, "gen_ai.", "gen_ai attribute leaked into the default convention")
	}

	_ = env.collector.DrainMetrics()
}

func requireStringAttr(t *testing.T, span *tracev1.Span, key string) string {
	t.Helper()
	for _, attr := range span.Attributes {
		if attr.Key == key {
			return attr.Value.GetStringValue()
		}
	}
	t.Fatalf("span %q has no attribute %q; got %s", span.Name, key, attrKeys(span))
	return ""
}

func requireIntAttr(t *testing.T, span *tracev1.Span, key string) int64 {
	t.Helper()
	for _, attr := range span.Attributes {
		if attr.Key == key {
			return attr.Value.GetIntValue()
		}
	}
	t.Fatalf("span %q has no attribute %q; got %s", span.Name, key, attrKeys(span))
	return 0
}

func attrKeys(span *tracev1.Span) []string {
	keys := make([]string, 0, len(span.Attributes))
	for _, attr := range span.Attributes {
		keys = append(keys, attr.Key)
	}
	return keys
}
