// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package tracing

import (
	"bytes"
	"context"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// otlpStringAttrs flattens an OTLP span's attributes to a string map.
func otlpStringAttrs(s *tracev1.Span) map[string]string {
	attrs := make(map[string]string, len(s.Attributes))
	for _, kv := range s.Attributes {
		attrs[kv.Key] = kv.Value.GetStringValue()
	}
	return attrs
}

// consumerIDMapping mirrors the deployed requestHeaderAttributes value: the
// gateway-injected consumer identity header mapped to consumer.id.
func consumerIDMapping() map[string]string {
	return map[string]string{"x-client-id": "consumer.id"}
}

// startChatSpanWithHeaders builds tracing from env with the given
// requestHeaderAttributes mapping, starts one chat-completion span with the
// given request headers, and returns its exported attributes.
func startChatSpanWithHeaders(t *testing.T, mapping, headers map[string]string) map[string]string {
	collector := testotel.StartOTLPCollector()
	t.Cleanup(collector.Close)
	collector.SetEnv(t.Setenv)

	result, err := NewTracingFromEnv(t.Context(), io.Discard, mapping)
	require.NoError(t, err)
	t.Cleanup(func() { _ = result.Shutdown(context.Background()) })

	span := result.ChatCompletionTracer().StartSpanAndInjectHeaders(
		t.Context(),
		headers,
		propagation.MapCarrier{},
		&openai.ChatCompletionRequest{Model: openai.ModelGPT5Nano},
		[]byte("{}"),
	)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)
	return otlpStringAttrs(v1Span)
}

// TestNewTracingFromEnv_UserIDHeader verifies that with
// AI_GATEWAY_TRACING_USER_ID_HEADER set, a request that carries both the
// gateway-injected consumer identity (x-client-id, post-auth) and the
// consumer-supplied user-id header gets user.id stamped on the SAME span
// as consumer.id. The env value is case-insensitive (Envoy delivers header
// keys lowercased).
func TestNewTracingFromEnv_UserIDHeader(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "X-AI-User-ID")

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": "auth0|64f1c2d3e4a5b6c7d8e9f0a1",
	})
	require.Equal(t, "librechat", attrs["consumer.id"])
	require.Equal(t, "auth0|64f1c2d3e4a5b6c7d8e9f0a1", attrs["user.id"])
}

// TestNewTracingFromEnv_UserIDHeader_CollisionKeepsExistingMapping pins the
// collision fail-safe end to end: when the env names the same header the
// deployment already maps to consumer.id (case differences included), the
// EXISTING mapping wins — consumer.id is stamped exactly as before and
// user.id never appears. Silently overriding would kill consumer
// attribution on every span from one misconfigured env var.
func TestNewTracingFromEnv_UserIDHeader_CollisionKeepsExistingMapping(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "X-Client-ID") // collides with x-client-id:consumer.id.

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": "alice",
	})
	require.Equal(t, "librechat", attrs["consumer.id"])
	require.NotContains(t, attrs, "user.id")
}

// TestWithUserIDHeaderFromEnv_CollisionLogsError pins the merge helper's
// collision behavior: the input mapping comes back unchanged (no user.id
// entry) and the error written to errOut names the env var, the colliding
// header, and both attributes involved.
func TestWithUserIDHeaderFromEnv_CollisionLogsError(t *testing.T) {
	t.Setenv(envUserIDHeader, " X-Client-ID ")
	var errBuf bytes.Buffer
	in := consumerIDMapping()
	out := withUserIDHeaderFromEnv(in, &errBuf)
	require.Equal(t, consumerIDMapping(), out)
	require.Equal(t, consumerIDMapping(), in)

	msg := errBuf.String()
	require.Contains(t, msg, envUserIDHeader)
	require.Contains(t, msg, `"x-client-id"`)
	require.Contains(t, msg, `"consumer.id"`)
	require.Contains(t, msg, `"user.id"`)
}

// TestNewTracingFromEnv_UserIDHeader_RequiresConsumerIdentity pins the trust
// boundary: without the gateway-injected consumer identity header on the
// request (i.e. traffic that did not pass consumer-key auth), the
// consumer-supplied user-id header is NOT stamped.
func TestNewTracingFromEnv_UserIDHeader_RequiresConsumerIdentity(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-ai-user-id": "mallory",
	})
	require.NotContains(t, attrs, "user.id")
	require.NotContains(t, attrs, "consumer.id")
}

// TestNewTracingFromEnv_UserIDHeader_EmptyConsumerIdentity pins that an
// EMPTY consumer-identity header (present, no value) does not count as
// authenticated consumer traffic: user.id is not stamped.
func TestNewTracingFromEnv_UserIDHeader_EmptyConsumerIdentity(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "",
		"x-ai-user-id": "alice",
	})
	require.NotContains(t, attrs, "user.id")
}

// TestNewTracingFromEnv_UserIDHeader_NoConsumerMappingFailsClosed pins the
// fail-closed side of the gate: when NO header is mapped to consumer.id at
// all (requestHeaderAttributes empty), user.id is never stamped — even for
// a request carrying both headers.
func TestNewTracingFromEnv_UserIDHeader_NoConsumerMappingFailsClosed(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")

	attrs := startChatSpanWithHeaders(t, nil, map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": "alice",
	})
	require.NotContains(t, attrs, "user.id")
}

// TestNewTracingFromEnv_UserIDHeader_DefaultOff pins the default: env unset
// (or empty) leaves upstream behavior exactly — the user-id header is
// ignored even when present alongside a valid consumer identity.
func TestNewTracingFromEnv_UserIDHeader_DefaultOff(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	// Pin to empty so an ambient value in the invoking shell/CI can't flip
	// the default under test (same idiom as the remote-parent-as-link pin).
	t.Setenv(envUserIDHeader, "")

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": "alice",
	})
	require.Equal(t, "librechat", attrs["consumer.id"])
	require.NotContains(t, attrs, "user.id")
}

// TestNewTracingFromEnv_UserIDHeader_Bounded verifies the value bounding: a
// hostile value is reduced to printable ASCII (control bytes and UTF-8
// multibyte sequences become '_') and truncated to maxUserIDValueLen bytes
// before it reaches the trace store.
func TestNewTracingFromEnv_UserIDHeader_Bounded(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")

	hostile := "alice\r\nx-injected: 1\x00\x1b[31mé" + strings.Repeat("A", maxUserIDValueLen)
	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": hostile,
	})

	got, ok := attrs["user.id"]
	require.True(t, ok)
	require.Len(t, got, maxUserIDValueLen)
	// \r\n, \x00, \x1b each become one '_'; "[31m" is printable and stays;
	// é is two UTF-8 bytes — two replacement chars.
	require.True(t, strings.HasPrefix(got, "alice__x-injected: 1__[31m__AAA"), got)
	for _, c := range []byte(got) {
		require.GreaterOrEqual(t, c, byte(0x20))
		require.LessOrEqual(t, c, byte(0x7e))
	}
}

// TestNewTracingFromEnv_UserIDHeader_NothingPrintable verifies a value with
// no printable content is dropped rather than stamped as underscores.
func TestNewTracingFromEnv_UserIDHeader_NothingPrintable(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")

	attrs := startChatSpanWithHeaders(t, consumerIDMapping(), map[string]string{
		"x-client-id":  "librechat",
		"x-ai-user-id": "\x00\x01 é ",
	})
	require.NotContains(t, attrs, "user.id")
	require.Equal(t, "librechat", attrs["consumer.id"])
}

// TestNewTracingFromEnv_UserIDHeader_MCPUntouched pins that the MCP tracer
// does not pick up the env-configured mapping: its meta-first lookup would
// bypass both the sanitizer and the consumer-auth gate, so the merge is
// request-tracers-only (see envUserIDHeader's doc).
func TestNewTracingFromEnv_UserIDHeader_MCPUntouched(t *testing.T) {
	internaltesting.ClearTestEnv(t)
	t.Setenv(envUserIDHeader, "x-ai-user-id")
	collector, tracing := newTracingFromEnvForTest(t, io.Discard)

	reqID, err := jsonrpc.MakeID("id")
	require.NoError(t, err)
	r := &jsonrpc.Request{ID: reqID, Method: "initialize"}
	p := &mcp.InitializeParams{Meta: map[string]any{"x-ai-user-id": "mallory"}}
	span := tracing.MCPTracer().StartSpanAndInjectMeta(t.Context(), r, p, nil)
	require.NotNil(t, span)
	span.EndSpan()

	v1Span := collector.TakeSpan()
	require.NotNil(t, v1Span)
	require.NotContains(t, otlpStringAttrs(v1Span), "user.id")
}

// TestWithUserIDHeaderFromEnv_DoesNotMutateInput guards the merge helper:
// the caller's map must come back untouched (the MCP tracer receives it).
func TestWithUserIDHeaderFromEnv_DoesNotMutateInput(t *testing.T) {
	t.Setenv(envUserIDHeader, " X-AI-User-ID ")
	in := consumerIDMapping()
	out := withUserIDHeaderFromEnv(in, io.Discard)
	require.Equal(t, consumerIDMapping(), in)
	require.Equal(t, map[string]string{
		"x-client-id":  "consumer.id",
		"x-ai-user-id": "user.id",
	}, out)

	t.Setenv(envUserIDHeader, "")
	require.Equal(t, consumerIDMapping(), withUserIDHeaderFromEnv(consumerIDMapping(), io.Discard))
}

// TestSanitizeUserIDValue is the byte-level table for the sanitizer.
func TestSanitizeUserIDValue(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"clean", "auth0|abc123", "auth0|abc123"},
		{"email", "alice@example.com", "alice@example.com"},
		{"control_bytes", "a\x00b\x1fc\x7fd", "a_b_c_d"},
		{"crlf", "a\r\nb", "a__b"},
		{"utf8_multibyte", "é", "__"},
		{"truncated", strings.Repeat("a", maxUserIDValueLen+10), strings.Repeat("a", maxUserIDValueLen)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sanitizeUserIDValue(tc.in))
		})
	}
}
