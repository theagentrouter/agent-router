// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

// stubMetrics implements metrics.MCPMetrics with no-ops.
type stubMetrics struct{}

func (s stubMetrics) WithRequestAttributes(*http.Request) metrics.MCPMetrics        { return s }
func (s stubMetrics) WithBackend(string) metrics.MCPMetrics                         { return s }
func (stubMetrics) RecordRequestDuration(context.Context, time.Time, mcpsdk.Params) {}
func (stubMetrics) RecordRequestErrorDuration(context.Context, time.Time, metrics.MCPErrorType, mcpsdk.Params) {
}
func (stubMetrics) RecordMethodCount(context.Context, string, mcpsdk.Params) {}
func (stubMetrics) RecordMethodErrorCount(context.Context, string, mcpsdk.Params, metrics.MCPStatusType) {
}
func (stubMetrics) RecordInitializationDuration(context.Context, time.Time, mcpsdk.Params) {}
func (stubMetrics) RecordClientCapabilities(context.Context, *mcpsdk.ClientCapabilities, mcpsdk.Params) {
}

func (stubMetrics) RecordServerCapabilities(context.Context, *mcpsdk.ServerCapabilities, mcpsdk.Params) {
}
func (stubMetrics) RecordProgress(context.Context, mcpsdk.Params) {}

func (stubMetrics) RecordNotificationStreamOpenAttempt(context.Context) {}
func (stubMetrics) RecordNotificationStreamOpenOutcome(context.Context, metrics.MCPNotificationStreamOutcome) {
}

func (stubMetrics) RecordNotificationStreamEnd(context.Context, time.Time, metrics.MCPNotificationStreamEndReason) {
}

func TestEncodeCapabilityFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		caps *mcpsdk.ServerCapabilities
		want string
	}{
		{name: "nil capabilities", caps: nil, want: "000"},
		{name: "empty capabilities", caps: &mcpsdk.ServerCapabilities{}, want: "000"},
		{name: "tools only", caps: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{},
		}, want: "001"},
		{name: "tools with list changed", caps: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{ListChanged: true},
		}, want: "003"},
		{name: "logging only", caps: &mcpsdk.ServerCapabilities{
			Logging: &mcpsdk.LoggingCapabilities{},
		}, want: "010"},
		{name: "resources with subscribe", caps: &mcpsdk.ServerCapabilities{
			Resources: &mcpsdk.ResourceCapabilities{Subscribe: true},
		}, want: "0a0"},
		{name: "completions only", caps: &mcpsdk.ServerCapabilities{
			Completions: &mcpsdk.CompletionCapabilities{},
		}, want: "100"},
		{name: "all capabilities", caps: &mcpsdk.ServerCapabilities{
			Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
			Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
			Logging:     &mcpsdk.LoggingCapabilities{},
			Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			Completions: &mcpsdk.CompletionCapabilities{},
		}, want: "1ff"},
		{name: "prompts without list changed", caps: &mcpsdk.ServerCapabilities{
			Prompts: &mcpsdk.PromptCapabilities{},
		}, want: "004"},
		{name: "resources with list changed only", caps: &mcpsdk.ServerCapabilities{
			Resources: &mcpsdk.ResourceCapabilities{ListChanged: true},
		}, want: "060"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := encodeCapabilityFlags(tc.caps)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDecodeCapabilityFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		hex  string
		want *mcpsdk.ServerCapabilities
	}{
		{name: "zero", hex: "000", want: &mcpsdk.ServerCapabilities{}},
		{name: "tools only", hex: "001", want: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{},
		}},
		{name: "tools with list changed", hex: "003", want: &mcpsdk.ServerCapabilities{
			Tools: &mcpsdk.ToolCapabilities{ListChanged: true},
		}},
		{name: "logging only", hex: "010", want: &mcpsdk.ServerCapabilities{
			Logging: &mcpsdk.LoggingCapabilities{},
		}},
		{name: "all capabilities", hex: "1ff", want: &mcpsdk.ServerCapabilities{
			Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
			Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
			Logging:     &mcpsdk.LoggingCapabilities{},
			Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			Completions: &mcpsdk.CompletionCapabilities{},
		}},
		{name: "invalid hex defaults to all", hex: "zzz", want: &mcpsdk.ServerCapabilities{
			Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
			Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
			Logging:     &mcpsdk.LoggingCapabilities{},
			Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			Completions: &mcpsdk.CompletionCapabilities{},
		}},
		{name: "empty string defaults to all", hex: "", want: &mcpsdk.ServerCapabilities{
			Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
			Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
			Logging:     &mcpsdk.LoggingCapabilities{},
			Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			Completions: &mcpsdk.CompletionCapabilities{},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := decodeCapabilityFlags(tc.hex)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEncodeDecodeCapabilityFlags_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []*mcpsdk.ServerCapabilities{
		nil,
		{},
		{Tools: &mcpsdk.ToolCapabilities{ListChanged: true}},
		{Logging: &mcpsdk.LoggingCapabilities{}},
		{Resources: &mcpsdk.ResourceCapabilities{Subscribe: true, ListChanged: true}},
		{Completions: &mcpsdk.CompletionCapabilities{}},
		{
			Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
			Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
			Logging:     &mcpsdk.LoggingCapabilities{},
			Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			Completions: &mcpsdk.CompletionCapabilities{},
		},
	}

	for _, caps := range cases {
		hex := encodeCapabilityFlags(caps)
		decoded := decodeCapabilityFlags(hex)
		// nil input encodes as "000" which decodes to empty (non-nil) ServerCapabilities.
		if caps == nil {
			require.Equal(t, &mcpsdk.ServerCapabilities{}, decoded)
		} else {
			require.Equal(t, caps, decoded)
		}
	}
}

func TestMergedCapabilities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		backends map[filterapi.MCPBackendName]*compositeSessionEntry
		want     *mcpsdk.ServerCapabilities
	}{
		{
			name:     "no backends",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{},
			want:     &mcpsdk.ServerCapabilities{},
		},
		{
			name: "single backend with all capabilities",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"b1": {capabilities: &mcpsdk.ServerCapabilities{
					Tools:   &mcpsdk.ToolCapabilities{ListChanged: true},
					Logging: &mcpsdk.LoggingCapabilities{},
				}},
			},
			want: &mcpsdk.ServerCapabilities{
				Tools:   &mcpsdk.ToolCapabilities{ListChanged: true},
				Logging: &mcpsdk.LoggingCapabilities{},
			},
		},
		{
			name: "backend with nil capabilities is skipped",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"b1": {capabilities: nil},
				"b2": {capabilities: &mcpsdk.ServerCapabilities{
					Logging: &mcpsdk.LoggingCapabilities{},
				}},
			},
			want: &mcpsdk.ServerCapabilities{
				Logging: &mcpsdk.LoggingCapabilities{},
			},
		},
		{
			name: "union of different capabilities",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"b1": {capabilities: &mcpsdk.ServerCapabilities{
					Tools:   &mcpsdk.ToolCapabilities{ListChanged: false},
					Logging: &mcpsdk.LoggingCapabilities{},
				}},
				"b2": {capabilities: &mcpsdk.ServerCapabilities{
					Tools:     &mcpsdk.ToolCapabilities{ListChanged: true},
					Resources: &mcpsdk.ResourceCapabilities{Subscribe: true},
				}},
			},
			want: &mcpsdk.ServerCapabilities{
				Tools:     &mcpsdk.ToolCapabilities{ListChanged: true},
				Logging:   &mcpsdk.LoggingCapabilities{},
				Resources: &mcpsdk.ResourceCapabilities{Subscribe: true},
			},
		},
		{
			name: "sub-fields are OR'd",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"b1": {capabilities: &mcpsdk.ServerCapabilities{
					Resources: &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: false},
				}},
				"b2": {capabilities: &mcpsdk.ServerCapabilities{
					Resources: &mcpsdk.ResourceCapabilities{ListChanged: false, Subscribe: true},
				}},
			},
			want: &mcpsdk.ServerCapabilities{
				Resources: &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
			},
		},
		{
			name: "union of extensions across backends",
			backends: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"b1": {capabilities: &mcpsdk.ServerCapabilities{
					Extensions: map[string]any{"io.modelcontextprotocol/ui": map[string]any{}},
				}},
				"b2": {capabilities: &mcpsdk.ServerCapabilities{
					Tools:      &mcpsdk.ToolCapabilities{ListChanged: true},
					Extensions: map[string]any{"example.com/other": map[string]any{}},
				}},
			},
			want: &mcpsdk.ServerCapabilities{
				Tools: &mcpsdk.ToolCapabilities{ListChanged: true},
				Extensions: map[string]any{
					"io.modelcontextprotocol/ui": map[string]any{},
					"example.com/other":          map[string]any{},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &session{perBackendSessions: tc.backends}
			got := s.mergedCapabilities()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBackendSessionIDs_Success(t *testing.T) {
	backendA := "backendA"
	backendB := "backendB"
	idA := "session-a"
	idB := "session-b"
	routeName := "some-route"
	composite := clientToGatewaySessionID(routeName + "@" + "subject" + "@" + backendA + ":" + base64.StdEncoding.EncodeToString([]byte(idA)) + "," + backendB + ":" + base64.StdEncoding.EncodeToString([]byte(idB)))
	m, route, subject, err := composite.backendSessionIDs()
	require.NoError(t, err)
	require.Equal(t, routeName, route)
	require.Equal(t, "subject", subject)
	require.Equal(t, idA, string(m[backendA].sessionID))
	require.Equal(t, idB, string(m[backendB].sessionID))
	// Old format without capability hex should default to all capabilities.
	require.NotNil(t, m[backendA].capabilities)
	require.NotNil(t, m[backendA].capabilities.Tools)
	require.NotNil(t, m[backendA].capabilities.Logging)
	require.NotNil(t, m[backendA].capabilities.Prompts)
	require.NotNil(t, m[backendA].capabilities.Resources)
	require.NotNil(t, m[backendA].capabilities.Completions)
}

func TestBackendSessionIDs_WithCapabilities(t *testing.T) {
	t.Parallel()
	routeName := "some-route"
	caps := &mcpsdk.ServerCapabilities{
		Tools:   &mcpsdk.ToolCapabilities{ListChanged: true},
		Logging: &mcpsdk.LoggingCapabilities{},
	}
	capHex := encodeCapabilityFlags(caps)
	// New format: backendName:base64SessionID:capHex
	composite := clientToGatewaySessionID(
		routeName + "@subject@" +
			"backendA:" + base64.StdEncoding.EncodeToString([]byte("sid-a")) + ":" + capHex + "," +
			"backendB:" + base64.StdEncoding.EncodeToString([]byte("sid-b")) + ":000",
	)
	m, route, subject, err := composite.backendSessionIDs()
	require.NoError(t, err)
	require.Equal(t, routeName, route)
	require.Equal(t, "subject", subject)
	require.Equal(t, "sid-a", string(m["backendA"].sessionID))
	require.Equal(t, "sid-b", string(m["backendB"].sessionID))
	// backendA should have tools + logging.
	require.NotNil(t, m["backendA"].capabilities.Tools)
	require.True(t, m["backendA"].capabilities.Tools.ListChanged)
	require.NotNil(t, m["backendA"].capabilities.Logging)
	require.Nil(t, m["backendA"].capabilities.Resources)
	// backendB has "000" = no capabilities.
	require.Nil(t, m["backendB"].capabilities.Tools)
	require.Nil(t, m["backendB"].capabilities.Logging)
}

func TestClientToGatewaySessionIDFromEntries_WithCapabilities(t *testing.T) {
	t.Parallel()
	caps := &mcpsdk.ServerCapabilities{
		Tools:   &mcpsdk.ToolCapabilities{ListChanged: true},
		Logging: &mcpsdk.LoggingCapabilities{},
	}
	entries := []compositeSessionEntry{
		{backendName: "b1", sessionID: "sid-1", capabilities: caps},
		{backendName: "b2", sessionID: "sid-2", capabilities: nil},
	}
	id := clientToGatewaySessionIDFromEntries("subj", entries, "route1")

	// Parse it back.
	m, route, subject, err := id.backendSessionIDs()
	require.NoError(t, err)
	require.Equal(t, "route1", route)
	require.Equal(t, "subj", subject)
	require.Equal(t, "sid-1", string(m["b1"].sessionID))
	require.Equal(t, "sid-2", string(m["b2"].sessionID))

	// b1 should have tools + logging from round-trip.
	require.NotNil(t, m["b1"].capabilities.Tools)
	require.True(t, m["b1"].capabilities.Tools.ListChanged)
	require.NotNil(t, m["b1"].capabilities.Logging)
	require.Nil(t, m["b1"].capabilities.Prompts)
	require.Nil(t, m["b1"].capabilities.Resources)
	require.Nil(t, m["b1"].capabilities.Completions)

	// b2 had nil capabilities, encoded as "000", decoded as empty.
	require.Nil(t, m["b2"].capabilities.Tools)
	require.Nil(t, m["b2"].capabilities.Logging)
}

func TestBackendSessionIDs_EmailSubject(t *testing.T) {
	t.Parallel()
	backendA := "backendA"
	backendB := "backendB"
	idA := "session-a"
	idB := "session-b"
	routeName := "some-route"
	for _, subject := range []string{
		"user@example.com",
		"",
	} {
		t.Run(subject, func(t *testing.T) {
			t.Parallel()
			composite := clientToGatewaySessionID(
				routeName + "@" + subject + "@" +
					backendA + ":" + base64.StdEncoding.EncodeToString([]byte(idA)) + "," +
					backendB + ":" + base64.StdEncoding.EncodeToString([]byte(idB)),
			)
			m, route, gotSubject, err := composite.backendSessionIDs()
			require.NoError(t, err)
			require.Equal(t, routeName, route)
			require.Equal(t, subject, gotSubject)
			require.Equal(t, idA, string(m[backendA].sessionID))
			require.Equal(t, idB, string(m[backendB].sessionID))
		})
	}
}

func TestBackendSessionIDs_Errors(t *testing.T) {
	for _, tc := range []struct {
		input  clientToGatewaySessionID
		expErr string
	}{
		// Without two '@' characters.
		{input: "no_at_chars", expErr: `invalid session ID: missing '@' separator`},
		// Only one '@' character.
		{input: "one@at_char", expErr: `invalid session ID: missing '@' separator`},
		// No ':'.
		{input: "@@missing_colon", expErr: `invalid session ID: missing ':' separator in backend session ID part "missing_colon"`},
		// Empty backend.
		{input: "@@:YWJj", expErr: "empty backend name"},
		{input: "@@backend:not-base64", expErr: `invalid session ID: failed to base64 decode session ID in part "backend:not-base64"`},
	} {
		t.Run(string(tc.input), func(t *testing.T) {
			_, _, _, err := tc.input.backendSessionIDs()
			require.ErrorContains(t, err, tc.expErr)
		})
	}
}

func TestSession_Close(t *testing.T) {
	var deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			if r.Header.Get(internalapi.MCPBackendHeader) == "backend1" || r.Header.Get(internalapi.MCPBackendHeader) == "backend2" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {
				sessionID: "s1",
			},
			"backend2": {
				sessionID: "s2",
			},
		},
		route: "test-route",
	}
	err := s.Close()
	require.NoError(t, err)
	require.Equal(t, int32(2), deletes.Load())
}

func TestSendRequestPerBackend_SetsOriginalPathHeaders(t *testing.T) {
	headersCh := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersCh <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	proxy.originalPath = "/mcp?foo=bar"

	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := s.sendRequestPerBackend(ctx, ch, "test-route", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)

	select {
	case hdr := <-headersCh:
		require.Equal(t, "/mcp?foo=bar", hdr.Get(internalapi.OriginalPathHeader))
		require.Equal(t, "/mcp?foo=bar", hdr.Get(internalapi.EnvoyOriginalPathHeader))
	case <-ctx.Done():
		require.Fail(t, "timed out waiting for backend request")
	}
}

func TestSendRequestPerBackend_PerBackendHeaders(t *testing.T) {
	headersCh := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersCh <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL

	t.Run("per-backend headers are forwarded to the matching backend", func(t *testing.T) {
		s := &session{
			reqCtx: proxy,
			perBackendExtraHeaders: map[filterapi.MCPBackendName]map[string]string{
				"backend1": {
					"X-Api-Key":      "secret123",
					"X-Backend-Auth": "Bearer tok",
				},
			},
		}
		ch := make(chan *backendEvent, 1)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		err := s.sendRequestPerBackend(ctx, ch, "test-route", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
			sessionID: "sess1",
		}, http.MethodGet, nil, nil)
		require.NoError(t, err)

		select {
		case hdr := <-headersCh:
			require.Equal(t, "secret123", hdr.Get("X-Api-Key"))
			require.Equal(t, "Bearer tok", hdr.Get("X-Backend-Auth"))
		case <-ctx.Done():
			require.Fail(t, "timed out waiting for backend request")
		}
	})

	t.Run("per-backend headers are NOT sent to a different backend", func(t *testing.T) {
		s := &session{
			reqCtx: proxy,
			perBackendExtraHeaders: map[filterapi.MCPBackendName]map[string]string{
				"backend1": {
					"X-Api-Key": "secret123",
				},
			},
		}
		ch := make(chan *backendEvent, 1)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		err := s.sendRequestPerBackend(ctx, ch, "test-route", filterapi.MCPBackend{Name: "backend2"}, &compositeSessionEntry{
			sessionID: "sess2",
		}, http.MethodGet, nil, nil)
		require.NoError(t, err)

		select {
		case hdr := <-headersCh:
			require.Empty(t, hdr.Get("X-Api-Key"), "per-backend header should NOT be sent to a different backend")
		case <-ctx.Done():
			require.Fail(t, "timed out waiting for backend request")
		}
	})

	t.Run("route-level and per-backend headers are both applied", func(t *testing.T) {
		s := &session{
			reqCtx:       proxy,
			extraHeaders: map[string]string{"X-Route-Header": "route-val"},
			perBackendExtraHeaders: map[filterapi.MCPBackendName]map[string]string{
				"backend1": {"X-Backend-Header": "backend-val"},
			},
		}
		ch := make(chan *backendEvent, 1)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		err := s.sendRequestPerBackend(ctx, ch, "test-route", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
			sessionID: "sess1",
		}, http.MethodGet, nil, nil)
		require.NoError(t, err)

		select {
		case hdr := <-headersCh:
			require.Equal(t, "route-val", hdr.Get("X-Route-Header"))
			require.Equal(t, "backend-val", hdr.Get("X-Backend-Header"))
		case <-ctx.Done():
			require.Fail(t, "timed out waiting for backend request")
		}
	})
}

func TestSendRequestPerBackend_AcceptEncoding(t *testing.T) {
	headersCh := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersCh <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL

	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := s.sendRequestPerBackend(ctx, ch, "test-route", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)

	select {
	case hdr := <-headersCh:
		ae := hdr.Get("Accept-Encoding")
		require.Contains(t, ae, "gzip", "Accept-Encoding must advertise gzip")
		require.Contains(t, ae, "br", "Accept-Encoding must advertise Brotli")
		require.NotContains(t, ae, "zstd", "Accept-Encoding must not advertise zstd")
	case <-ctx.Done():
		require.Fail(t, "timed out waiting for backend request")
	}
}

func TestSendRequestPerBackend_GzipDecompression(t *testing.T) {
	id1, _ := jsonrpc.MakeID("1")
	msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "ping", ID: id1})
	sseBody := "event: message\ndata: " + string(msg1) + "\n\n"

	// Compress the SSE body with gzip.
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	_, err := gw.Write([]byte(sseBody))
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = s.sendRequestPerBackend(ctx, ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)
	close(ch)
	var events []*backendEvent
	for e := range ch {
		events = append(events, e)
	}
	require.Len(t, events, 1, "expected 1 event from gzip-compressed response")
	require.Equal(t, "message", events[0].event)
	require.Len(t, events[0].messages, 1)
	req, ok := events[0].messages[0].(*jsonrpc.Request)
	require.True(t, ok)
	require.Equal(t, "ping", req.Method)
}

func TestSendRequestPerBackend_BrotliDecompression(t *testing.T) {
	id1, _ := jsonrpc.MakeID("1")
	msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "ping", ID: id1})
	sseBody := "event: message\ndata: " + string(msg1) + "\n\n"

	// Compress the SSE body with Brotli.
	var compressed bytes.Buffer
	bw := brotli.NewWriter(&compressed)
	_, err := bw.Write([]byte(sseBody))
	require.NoError(t, err)
	require.NoError(t, bw.Close())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "br")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err = s.sendRequestPerBackend(ctx, ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)
	close(ch)
	var events []*backendEvent
	for e := range ch {
		events = append(events, e)
	}
	require.Len(t, events, 1, "expected 1 event from Brotli-compressed response")
	require.Equal(t, "message", events[0].event)
	require.Len(t, events[0].messages, 1)
	req, ok := events[0].messages[0].(*jsonrpc.Request)
	require.True(t, ok)
	require.Equal(t, "ping", req.Method)
}

func TestSendRequestPerBackend_BOMPrefixedJSON(t *testing.T) {
	id1, _ := jsonrpc.MakeID("1")
	msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: id1, Result: []byte(`{"ok":true}`)})

	bomBody := append([]byte{0xEF, 0xBB, 0xBF}, msg1...)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bomBody)
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := s.sendRequestPerBackend(ctx, ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)
	close(ch)
	var events []*backendEvent
	for e := range ch {
		events = append(events, e)
	}
	require.Len(t, events, 1, "expected 1 event from BOM-prefixed JSON response")
	require.Equal(t, "message", events[0].event)
	require.Len(t, events[0].messages, 1)
	resp, ok := events[0].messages[0].(*jsonrpc.Response)
	require.True(t, ok)
	require.Equal(t, id1, resp.ID)
}

func TestSendRequestPerBackend_JSONContentTypeWithCharset(t *testing.T) {
	id1, _ := jsonrpc.MakeID("1")
	msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: id1, Result: []byte(`{"ok":true}`)})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(msg1)
	}))
	defer server.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = server.URL
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := s.sendRequestPerBackend(ctx, ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)
	close(ch)
	var events []*backendEvent
	for e := range ch {
		events = append(events, e)
	}
	require.Len(t, events, 1, "expected 1 event from application/json;charset=UTF-8 response")
	require.Equal(t, "message", events[0].event)
	require.Len(t, events[0].messages, 1)
	resp, ok := events[0].messages[0].(*jsonrpc.Response)
	require.True(t, ok)
	require.Equal(t, id1, resp.ID)
}

func TestHandleNotificationsPerBackend_SSE(t *testing.T) {
	// Provide two SSE events with valid JSON-RPC requests then close.
	id1, _ := jsonrpc.MakeID("1")
	id2, _ := jsonrpc.MakeID("2")
	msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "ping", ID: id1})
	msg2, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "pong", ID: id2})
	sseBody := "event: ping\n" + "data: " + string(msg1) + "\n\n" + "event: pong\n" + "data: " + string(msg2) + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "text/event-stream, application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunkSize := len(sseBody) / 3
		for i := 0; i < len(sseBody); i += chunkSize {
			end := i + chunkSize
			if end > len(sseBody) {
				end = len(sseBody)
			}
			_, _ = w.Write([]byte(sseBody[i:end]))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer server.Close()
	l := slog.Default()
	proxy := &mcpRequestContext{metrics: stubMetrics{}, ProxyConfig: &ProxyConfig{mcpProxyConfig: &mcpProxyConfig{backendListenerAddr: server.URL}, l: l}}
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := s.sendRequestPerBackend(ctx, ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.NoError(t, err)
	close(ch)
	count := 0
	for range ch {
		count++
	}
	require.Equal(t, 2, count, "expected 2 events")
}

func TestSession_StreamNotifications(t *testing.T) {
	tests := []struct {
		name               string
		eventInterval      time.Duration
		deadline           time.Duration
		heartbeatInterval  time.Duration
		expectedHeartbeats bool
	}{
		// the default heartbeat interval is 1 second, but the events will come faster, so
		// we don't expect any heartbeats.
		{"fast events", 10 * time.Millisecond, 500 * time.Millisecond, 10 * time.Second, false},
		// configure a heartbeat interval faster than the event interval, so we expect heartbeats.
		{"slow events", 20 * time.Millisecond, 500 * time.Millisecond, 10 * time.Millisecond, true},
		// disable heartbeats. Even though events come in slowly, we don't expect heartbeats.
		{"no heartbeats", 20 * time.Millisecond, 500 * time.Millisecond, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Override the default heartbeat interval for testing.
			originalHeartbeatInterval := heartbeatInterval
			heartbeatInterval = tc.heartbeatInterval
			t.Cleanup(func() { heartbeatInterval = originalHeartbeatInterval })

			// Single backend streaming two events with valid messages.
			id1, _ := jsonrpc.MakeID("1")
			id2, _ := jsonrpc.MakeID("2")
			msg1, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "a1", ID: id1})
			msg2, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "a2", ID: id2})
			body := "event: a1\n" + "data: " + string(msg1) + "\n\n" + "event: a2\n" + "data: " + string(msg2) + "\n\n"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.Header.Get(internalapi.MCPBackendHeader) != "backend1" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				chunkSize := len(body) / 3
				for i := 0; i < len(body); i += chunkSize {
					end := i + chunkSize
					if end > len(body) {
						end = len(body)
					}
					_, _ = w.Write([]byte(body[i:end]))
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
					time.Sleep(tc.eventInterval)
				}
			}))
			defer srv.Close()
			proxy := newTestMCPProxy()
			proxy.backendListenerAddr = srv.URL

			s := &session{
				reqCtx: proxy,
				perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
					"backend1": {
						sessionID: "s1",
					},
				},
				route: "test-route",
			}
			rr := httptest.NewRecorder()
			ctx, cancel := context.WithTimeout(t.Context(), tc.deadline)
			defer cancel()
			err2 := s.streamNotifications(ctx, rr, proxy.toolChangeSignaler)
			require.ErrorIs(t, err2, context.DeadlineExceeded)
			out := rr.Body.String()
			require.Contains(t, out, "event: a1")
			require.Contains(t, out, "event: a2")
			heartbeatCount := strings.Count(out, `"method":"ping"`)

			if tc.expectedHeartbeats {
				require.Greater(t, heartbeatCount, 1, "expected some heartbeats after the initial one")
			} else {
				require.Equal(t, 1, heartbeatCount, "expected only the initial heartbeat")
			}
		})
	}
}

func TestNotifyToolsChanged(t *testing.T) {
	var (
		reloadConfig atomic.Bool
		proxy        = newTestMCPProxy()
		cfg          = ProxyConfig{
			toolChangeSignaler: proxy.toolChangeSignaler,
			mcpProxyConfig:     proxy.mcpProxyConfig,
		}
		s = &session{
			reqCtx: proxy,
			route:  "test-route",
			perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
				"backend1": {sessionID: "s1"},
			},
		}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// if the test wants to reload config, trigger it once the stream is open, to better simulate
		// changes when there is an active streaming session.
		// wait a bit and trigger the config change.
		if reloadConfig.Load() {
			time.Sleep(50 * time.Millisecond)
			require.NoError(t, cfg.LoadConfig(t.Context(),
				// Clear all the routes -> should trigger a tools changed notification.
				&filterapi.Config{MCPConfig: &filterapi.MCPConfig{}}),
			)
		}
	}))
	proxy.backendListenerAddr = srv.URL

	t.Run("no tool changes by default", func(t *testing.T) {
		rr := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		t.Cleanup(cancel)
		err := s.streamNotifications(ctx, rr, proxy.toolChangeSignaler)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		out := rr.Body.String()
		require.NotContains(t, out, `"id":"`+envoyAIGatewayServerToClientToolsChangedRequestIDPrefix)
		require.NotContains(t, out, `"method":"notifications/tools/list_changed"`)
	})

	t.Run("notify tools changed", func(t *testing.T) {
		reloadConfig.Store(true)
		rr := httptest.NewRecorder()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		t.Cleanup(cancel)
		err := s.streamNotifications(ctx, rr, proxy.toolChangeSignaler)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		out := rr.Body.String()
		require.Contains(t, out, `"id":"`+envoyAIGatewayServerToClientToolsChangedRequestIDPrefix)
		require.Contains(t, out, `"method":"notifications/tools/list_changed"`)
	})
}

func TestStreamNotifications_AllBackends405(t *testing.T) {
	// When all backends return 405 for GET, streamNotifications should NOT return
	// immediately. It should keep the SSE connection alive with heartbeats until the
	// context is cancelled. This prevents a rapid reconnection loop when backends
	// don't support the GET SSE notification stream.
	originalHeartbeatInterval := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = originalHeartbeatInterval })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	proxy := newTestMCPProxy()
	proxy.backendListenerAddr = srv.URL

	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {backendName: "backend1", sessionID: "s1"},
		},
		route: "test-route",
	}

	rr := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := s.streamNotifications(ctx, rr, proxy.toolChangeSignaler)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	out := rr.Body.String()
	// Should have the initial heartbeat plus additional ones while waiting.
	heartbeatCount := strings.Count(out, `"method":"ping"`)
	require.Greater(t, heartbeatCount, 1, "expected heartbeats while waiting; got output: %s", out)
}

func TestSendRequestPerBackend_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()
	l := slog.Default()
	proxy := &mcpRequestContext{ProxyConfig: &ProxyConfig{mcpProxyConfig: &mcpProxyConfig{backendListenerAddr: server.URL}, l: l}, metrics: stubMetrics{}}
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 1)
	cse := &compositeSessionEntry{
		sessionID: "sess1",
	}
	err2 := s.sendRequestPerBackend(t.Context(), ch, "route1", filterapi.MCPBackend{Name: "backend1"}, cse, http.MethodGet, nil, nil)
	require.Error(t, err2)
	require.Contains(t, err2.Error(), "failed with status code")
}

func TestSendRequestPerBackend_EOF(t *testing.T) {
	// Immediate EOF (empty body) should return nil (no error) after loop breaks with EOF.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// No writes -> body closes -> EOF.
	}))
	defer server.Close()
	l := slog.Default()
	proxy := &mcpRequestContext{ProxyConfig: &ProxyConfig{mcpProxyConfig: &mcpProxyConfig{backendListenerAddr: server.URL}, l: l}, metrics: stubMetrics{}}
	s := &session{reqCtx: proxy}
	ch := make(chan *backendEvent, 1)
	err2 := s.sendRequestPerBackend(t.Context(), ch, "route1", filterapi.MCPBackend{Name: "backend1"}, &compositeSessionEntry{
		sessionID: "sess1",
	}, http.MethodGet, nil, nil)
	require.True(t, err2 == nil || errors.Is(err2, io.EOF), "unexpected error: %v", err2)
}

func TestGetHeartbeatInterval(t *testing.T) {
	defaultInterval := 1 * time.Minute

	tests := []struct {
		name     string
		env      string
		expected time.Duration
	}{
		{"unset", "", defaultInterval},
		{"invalid", "invalid", defaultInterval},
		{"zero", "0s", 0},
		{"value", "5m", 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("MCP_PROXY_HEARTBEAT_INTERVAL", tt.env)
			}
			require.Equal(t, tt.expected, getHeartbeatInterval(defaultInterval))
		})
	}
}

const (
	streamAttemptsMetric = "mcp.notification_stream.open.attempts"
	streamOutcomesMetric = "mcp.notification_stream.open.outcomes"
	streamActiveMetric   = "mcp.notification_stream.active"
	streamDurationMetric = "mcp.notification_stream.duration"
)

func backendAttrs(backend string) attribute.Set {
	return attribute.NewSet(attribute.String("mcp.backend", backend))
}

func outcomeAttrs(backend string, outcome metrics.MCPNotificationStreamOutcome) attribute.Set {
	return attribute.NewSet(
		attribute.String("mcp.backend", backend),
		attribute.String("mcp.notification_stream.outcome", string(outcome)),
	)
}

func endReasonAttrs(backend string, reason metrics.MCPNotificationStreamEndReason) attribute.Set {
	return attribute.NewSet(
		attribute.String("mcp.backend", backend),
		attribute.String("mcp.notification_stream.end_reason", string(reason)),
	)
}

// lookupStreamMetric returns the value of a sum metric, or the count of a histogram, for the given
// attributes. Unlike the testotel helpers it does not fail when the data point does not exist yet,
// so it can be used for polling and for asserting absence.
func lookupStreamMetric(t *testing.T, mr *sdkmetric.ManualReader, name string, attrs attribute.Set) (float64, bool) {
	t.Helper()
	var data metricdata.ResourceMetrics
	require.NoError(t, mr.Collect(t.Context(), &data))
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[float64]:
				for _, dp := range d.DataPoints {
					if dp.Attributes.Equals(&attrs) {
						return dp.Value, true
					}
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					if dp.Attributes.Equals(&attrs) {
						return float64(dp.Count), true
					}
				}
			}
		}
	}
	return 0, false
}

// totalStreamOutcomes returns the number of open outcomes recorded for the backend across all outcome values.
func totalStreamOutcomes(t *testing.T, mr *sdkmetric.ManualReader, backend string) float64 {
	t.Helper()
	var data metricdata.ResourceMetrics
	require.NoError(t, mr.Collect(t.Context(), &data))
	var total float64
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != streamOutcomesMetric {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[float64]).DataPoints {
				if v, ok := dp.Attributes.Value("mcp.backend"); ok && v.AsString() == backend {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func requireStreamMetricEventually(t *testing.T, mr *sdkmetric.ManualReader, name string, attrs attribute.Set, want float64) {
	t.Helper()
	require.Eventually(t, func() bool {
		v, ok := lookupStreamMetric(t, mr, name, attrs)
		return ok && v == want
	}, 5*time.Second, 10*time.Millisecond, "%s %v never reached %v", name, attrs.ToSlice(), want)
}

// newNotificationStreamTestProxy returns a proxy that records metrics into the returned reader
// and sends backend requests to a test server running handler. The request context seen by
// handler is also cancelled at test cleanup, so handlers that hold the response until the
// request is cancelled cannot block server shutdown even if the client never cancels.
func newNotificationStreamTestProxy(t *testing.T, handler http.HandlerFunc) (*mcpRequestContext, *sdkmetric.ManualReader) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-release:
				cancel()
			case <-ctx.Done():
			}
		}()
		handler(w, r.WithContext(ctx))
	}))
	t.Cleanup(func() {
		close(release)
		srv.CloseClientConnections()
		srv.Close()
	})
	mr := sdkmetric.NewManualReader()
	proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
	proxy.backendListenerAddr = srv.URL
	return proxy, mr
}

// openNotificationStream opens the legacy GET notification stream to backend1.
func openNotificationStream(ctx context.Context, proxy *mcpRequestContext) error {
	s := &session{reqCtx: proxy}
	return s.sendRequestPerBackend(ctx, make(chan *backendEvent, 10), "route1", filterapi.MCPBackend{Name: "backend1"},
		&compositeSessionEntry{sessionID: "sess1"}, http.MethodGet, nil, nil)
}

// runInBackground runs fn with a cancellable context. The returned wait function returns fn's
// result, failing the test if fn does not return within 5s. Only an actual completion is cached,
// so cleanup still cancels and then joins the worker with a bounded wait after an earlier timeout.
func runInBackground(t *testing.T, fn func(context.Context) error) (cancel context.CancelFunc, wait func() error) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	var (
		finished bool
		result   error
	)
	wait = func() error {
		if finished {
			return result
		}
		select {
		case result = <-done:
			finished = true
		case <-time.After(5 * time.Second):
			t.Error("background worker did not return within 5s")
		}
		return result
	}
	t.Cleanup(func() {
		cancel()
		_ = wait()
	})
	return cancel, wait
}

func sseTestEvent(t *testing.T) string {
	id, _ := jsonrpc.MakeID("1")
	msg, err := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "a1", ID: id})
	require.NoError(t, err)
	return "event: a1\ndata: " + string(msg) + "\n\n"
}

func TestStreamNotifications_Metrics_HealthyAndRejectedBackends(t *testing.T) {
	originalHeartbeatInterval := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = originalHeartbeatInterval })

	event := sseTestEvent(t)
	proxy, mr := newNotificationStreamTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(internalapi.MCPBackendHeader) == "backend2" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// backend1 keeps a healthy stream open until the client goes away.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(event))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {backendName: "backend1", sessionID: "s1"},
			"backend2": {backendName: "backend2", sessionID: "s2"},
		},
		route: "test-route",
	}
	cancel, wait := runInBackground(t, func(ctx context.Context) error {
		return s.streamNotifications(ctx, httptest.NewRecorder(), proxy.toolChangeSignaler)
	})

	requireStreamMetricEventually(t, mr, streamActiveMetric, backendAttrs("backend1"), 1)
	requireStreamMetricEventually(t, mr, streamOutcomesMetric, outcomeAttrs("backend2", metrics.MCPNotificationStreamOutcomeUnsupported), 1)
	for _, b := range []string{"backend1", "backend2"} {
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamAttemptsMetric, backendAttrs(b)))
		require.Equal(t, float64(1), totalStreamOutcomes(t, mr, b))
	}
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
		outcomeAttrs("backend1", metrics.MCPNotificationStreamOutcomeOpened)))
	// The rejected backend never opened a stream, so it has no active streams.
	_, ok := lookupStreamMetric(t, mr, streamActiveMetric, backendAttrs("backend2"))
	require.False(t, ok)

	cancel()
	require.ErrorIs(t, wait(), context.Canceled)
	requireStreamMetricEventually(t, mr, streamDurationMetric, endReasonAttrs("backend1", metrics.MCPNotificationStreamEndReasonCancelled), 1)
	require.Equal(t, float64(0), testotel.GetCounterValue(t, mr, streamActiveMetric, backendAttrs("backend1")))
}

func TestStreamNotifications_Metrics_AllBackendStreamsEndWhileHeartbeatsContinue(t *testing.T) {
	originalHeartbeatInterval := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = originalHeartbeatInterval })

	event := sseTestEvent(t)
	// Every backend sends one event and closes its stream normally.
	proxy, mr := newNotificationStreamTestProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(event))
	})
	s := &session{
		reqCtx: proxy,
		perBackendSessions: map[filterapi.MCPBackendName]*compositeSessionEntry{
			"backend1": {backendName: "backend1", sessionID: "s1"},
			"backend2": {backendName: "backend2", sessionID: "s2"},
		},
		route: "test-route",
	}
	rr := &lockedRecorder{rec: httptest.NewRecorder()}
	cancel, wait := runInBackground(t, func(ctx context.Context) error {
		return s.streamNotifications(ctx, rr, proxy.toolChangeSignaler)
	})

	backends := []string{"backend1", "backend2"}
	for _, b := range backends {
		requireStreamMetricEventually(t, mr, streamDurationMetric, endReasonAttrs(b, metrics.MCPNotificationStreamEndReasonEOF), 1)
	}
	// All backend streams have ended, but the client stream stays open and keeps receiving heartbeats.
	heartbeatsAfterEnd := rr.count(`"method":"ping"`)
	require.Eventually(t, func() bool {
		return rr.count(`"method":"ping"`) > heartbeatsAfterEnd
	}, 5*time.Second, 10*time.Millisecond)

	for _, b := range backends {
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamAttemptsMetric, backendAttrs(b)))
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
			outcomeAttrs(b, metrics.MCPNotificationStreamOutcomeOpened)))
		require.Equal(t, float64(0), testotel.GetCounterValue(t, mr, streamActiveMetric, backendAttrs(b)))
	}

	cancel()
	require.ErrorIs(t, wait(), context.Canceled, "streamNotifications must only return on cancellation")
}

// lockedRecorder is an http.ResponseWriter that can be read while streamNotifications writes to it.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Header()
}

func (l *lockedRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(b)
}

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

func (l *lockedRecorder) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.rec.Body.String(), substr)
}

func TestSendRequestPerBackend_NotificationStreamOpenOutcomes(t *testing.T) {
	id, _ := jsonrpc.MakeID("1")
	ping, _ := jsonrpc.EncodeMessage(&jsonrpc.Request{Method: "ping", ID: id})
	tests := []struct {
		name                  string
		status                int
		contentType, encoding string
		contentLength, body   string
		want                  metrics.MCPNotificationStreamOutcome
	}{
		{name: "202", status: http.StatusAccepted, want: metrics.MCPNotificationStreamOutcomeUnsupported},
		{name: "204", status: http.StatusNoContent, want: metrics.MCPNotificationStreamOutcomeUnsupported},
		{name: "405", status: http.StatusMethodNotAllowed, want: metrics.MCPNotificationStreamOutcomeUnsupported},
		{name: "404", status: http.StatusNotFound, want: metrics.MCPNotificationStreamOutcomeHTTP4xx},
		{name: "500", status: http.StatusInternalServerError, want: metrics.MCPNotificationStreamOutcomeHTTP5xx},
		{name: "503", status: http.StatusServiceUnavailable, want: metrics.MCPNotificationStreamOutcomeHTTP5xx},
		{name: "201", status: http.StatusCreated, want: metrics.MCPNotificationStreamOutcomeHTTPOther},
		{
			name: "single JSON-RPC message instead of a stream", status: http.StatusOK,
			contentType: "application/json", body: string(ping), want: metrics.MCPNotificationStreamOutcomeUnsupported,
		},
		{
			name: "truncated JSON body", status: http.StatusOK, contentType: "application/json",
			contentLength: "100", body: `{"jsonrpc":`, want: metrics.MCPNotificationStreamOutcomeTransportError,
		},
		{
			name: "204 with gzip encoding and empty body", status: http.StatusNoContent,
			encoding: "gzip", want: metrics.MCPNotificationStreamOutcomeUnsupported,
		},
		{
			name: "503 with invalid gzip body", status: http.StatusServiceUnavailable,
			encoding: "gzip", body: "not gzip", want: metrics.MCPNotificationStreamOutcomeHTTP5xx,
		},
		{
			name: "200 with invalid gzip body", status: http.StatusOK, contentType: "text/event-stream",
			encoding: "gzip", body: "not gzip", want: metrics.MCPNotificationStreamOutcomeTransportError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy, mr := newNotificationStreamTestProxy(t, func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range map[string]string{"Content-Type": tc.contentType, "Content-Encoding": tc.encoding, "Content-Length": tc.contentLength} {
					if v != "" {
						w.Header().Set(k, v)
					}
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			_ = openNotificationStream(t.Context(), proxy)

			require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamAttemptsMetric, backendAttrs("backend1")))
			require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric, outcomeAttrs("backend1", tc.want)))
			require.Equal(t, float64(1), totalStreamOutcomes(t, mr, "backend1"), "exactly one outcome per attempt")
			_, ok := lookupStreamMetric(t, mr, streamActiveMetric, backendAttrs("backend1"))
			require.False(t, ok, "a stream that never opened must not be counted as active")
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSendRequestPerBackend_NotificationStreamTransportError(t *testing.T) {
	mr := sdkmetric.NewManualReader()
	proxy := newTestMCPProxyWithOTEL(mr, noopTracer)
	proxy.client = http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	require.Error(t, openNotificationStream(t.Context(), proxy))
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
		outcomeAttrs("backend1", metrics.MCPNotificationStreamOutcomeTransportError)))
}

func TestSendRequestPerBackend_NotificationStreamEndReasons(t *testing.T) {
	event := sseTestEvent(t)
	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
		want    metrics.MCPNotificationStreamEndReason
	}{
		{
			name: "valid final event without trailing blank line",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(strings.TrimSuffix(event, "\n\n")))
			},
			want: metrics.MCPNotificationStreamEndReasonEOF,
		},
		{
			name: "malformed final event without trailing blank line",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(event + "data: {invalid json}"))
			},
			want: metrics.MCPNotificationStreamEndReasonError,
		},
		{
			name: "malformed complete event",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {invalid json}\n\n"))
			},
			wantErr: true,
			want:    metrics.MCPNotificationStreamEndReasonError,
		},
		{
			name: "connection cut mid-chunk",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				conn, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
				_, _ = buf.WriteString("20\r\nevent: a1\n")
				_ = buf.Flush()
			},
			want: metrics.MCPNotificationStreamEndReasonError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proxy, mr := newNotificationStreamTestProxy(t, tc.handler)
			err := openNotificationStream(t.Context(), proxy)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
				outcomeAttrs("backend1", metrics.MCPNotificationStreamOutcomeOpened)))
			count, _ := testotel.GetHistogramValues(t, mr, streamDurationMetric, endReasonAttrs("backend1", tc.want))
			require.Equal(t, uint64(1), count)
			require.Equal(t, float64(0), testotel.GetCounterValue(t, mr, streamActiveMetric, backendAttrs("backend1")))
		})
	}
}

func TestSendRequestPerBackend_NotificationStreamCancellation(t *testing.T) {
	t.Run("before open", func(t *testing.T) {
		entered := make(chan struct{})
		proxy, mr := newNotificationStreamTestProxy(t, func(_ http.ResponseWriter, r *http.Request) {
			close(entered)
			<-r.Context().Done() // Withhold the response, so the stream never opens.
		})
		cancel, wait := runInBackground(t, func(ctx context.Context) error { return openNotificationStream(ctx, proxy) })

		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "backend request was not received")
		}
		// The attempt is counted while the response is still withheld.
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamAttemptsMetric, backendAttrs("backend1")))
		require.Equal(t, float64(0), totalStreamOutcomes(t, mr, "backend1"))

		cancel()
		require.NoError(t, wait())
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
			outcomeAttrs("backend1", metrics.MCPNotificationStreamOutcomeCancelled)))
		_, ok := lookupStreamMetric(t, mr, streamActiveMetric, backendAttrs("backend1"))
		require.False(t, ok)
	})

	t.Run("after open", func(t *testing.T) {
		proxy, mr := newNotificationStreamTestProxy(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		cancel, wait := runInBackground(t, func(ctx context.Context) error { return openNotificationStream(ctx, proxy) })

		requireStreamMetricEventually(t, mr, streamActiveMetric, backendAttrs("backend1"), 1)
		const minLifetime = 50 * time.Millisecond
		time.Sleep(minLifetime)
		cancel()
		require.NoError(t, wait())

		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, streamOutcomesMetric,
			outcomeAttrs("backend1", metrics.MCPNotificationStreamOutcomeOpened)))
		count, sum := testotel.GetHistogramValues(t, mr, streamDurationMetric,
			endReasonAttrs("backend1", metrics.MCPNotificationStreamEndReasonCancelled))
		require.Equal(t, uint64(1), count)
		// The lifetime is measured from when the stream opened.
		require.GreaterOrEqual(t, sum, minLifetime.Seconds())
		require.Less(t, sum, 60.0)
		require.Equal(t, float64(0), testotel.GetCounterValue(t, mr, streamActiveMetric, backendAttrs("backend1")))
	})
}

func TestIsCleanEOF(t *testing.T) {
	bad := errors.New("bad frame")
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "EOF", err: io.EOF, want: true},
		{name: "wrapped EOF", err: fmt.Errorf("read: %w", io.EOF), want: true},
		{name: "EOF joined with nil", err: errors.Join(io.EOF, nil), want: true},
		{name: "EOF joined with parse error", err: errors.Join(io.EOF, bad), want: false},
		{name: "wrapped join with parse error", err: fmt.Errorf("read: %w", errors.Join(io.EOF, bad)), want: false},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, want: false},
		{name: "other error", err: bad, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isCleanEOF(tc.err))
		})
	}
}

func TestSendRequestPerBackend_POSTDoesNotRecordNotificationStreamMetrics(t *testing.T) {
	proxy, mr := newNotificationStreamTestProxy(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{}}\n\n"))
	})
	s := &session{reqCtx: proxy}
	id, _ := jsonrpc.MakeID("1")
	err := s.sendRequestPerBackend(t.Context(), make(chan *backendEvent, 1), "route1", filterapi.MCPBackend{Name: "backend1"},
		&compositeSessionEntry{sessionID: "sess1"}, http.MethodPost, &jsonrpc.Request{Method: "tools/list", ID: id}, nil)
	require.NoError(t, err)

	var data metricdata.ResourceMetrics
	require.NoError(t, mr.Collect(t.Context(), &data))
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			require.NotContains(t, m.Name, "mcp.notification_stream.")
		}
	}
}
