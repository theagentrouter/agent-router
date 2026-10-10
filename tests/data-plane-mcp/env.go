// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplanemcp

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
	"github.com/envoyproxy/ai-gateway/internal/version"
	"github.com/envoyproxy/ai-gateway/tests/internal/dataplaneenv"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

// envoyConfig is the embedded Envoy configuration template.
//
//go:embed envoy.yaml
var envoyConfig string

// MCPSuite is a test suite for testing MCP client and server interactions using the server-implementation in testcmp package.
//
// TODO: move this to testmcp package so that we could reuse the same tests in the end-to-end tests.
type mcpEnv struct {
	client            *mcp.Client
	mcp1, mcp2        *mcp.Server
	mux               sync.Mutex
	extProcMetricsURL string
	baseURL           string
	env               *dataplaneenv.TestEnvironment
	sessions          map[string]*mcpSession // ID -> session.
	writeTimeout      time.Duration
	collector         *testotel.OTLPCollector
}

// mcpSession wraps an MCP client session and its associated state for testing.
//
// See the comment on ProgressNotificationHandler in [MCPSuite.SetupSuite] for why we need this wrapper.
// Otherwise, we could pass the handler option directly to the [MCPSuite.newSession] call.
//
// **NOTE*** Do not add any direct access to the in-memory server session. Otherwise, the tests will result in
// not being able to run in end-to-end tests. The test code must solely operate through the client side sessions.
type mcpSession struct {
	session *mcp.ClientSession
	// TODO: merge them into one chan for simplicity?
	toolListChangedNotifications     chan *mcp.ToolListChangedRequest
	progressNotifications            chan *mcp.ProgressNotificationClientRequest
	promptListChangedNotifications   chan *mcp.PromptListChangedRequest
	resourceUpdatedNotifications     chan *mcp.ResourceUpdatedNotificationRequest
	resourceListChangedNotifications chan *mcp.ResourceListChangedRequest
	loggingNotification              chan *mcp.LoggingMessageRequest
	createMessageRequests            chan *mcp.CreateMessageRequest
	elicitRequests                   chan *mcp.ElicitRequest
}

const (
	mcpDefaultRootName = "test-root"
	mcpDefaultRootURI  = "foo://bar"
)

func requireNewMCPEnv(t *testing.T, forceJSONResponse bool, writeTimeout time.Duration, path string, extprocArgs ...string) *mcpEnv {
	t.Helper()
	return requireNewMCPEnvWithExtProcEnv(t, nil, forceJSONResponse, writeTimeout, path, extprocArgs...)
}

// requireNewMCPEnvWithExtProcEnv is requireNewMCPEnv with extra environment
// variables for the ExtProc process, used to exercise the tracing semantic
// convention selector end to end.
func requireNewMCPEnvWithExtProcEnv(t *testing.T, extraEnv []string, forceJSONResponse bool, writeTimeout time.Duration, path string, extprocArgs ...string) *mcpEnv {
	t.Helper()

	internaltesting.ClearTestEnv(t)

	collector := testotel.StartOTLPCollector()
	t.Cleanup(collector.Close)
	mcpConfig := &filterapi.MCPConfig{
		BackendListenerAddr: "http://127.0.0.1:9999",
		Routes: []filterapi.MCPRoute{
			{
				Name: "test-route",
				Backends: []filterapi.MCPBackend{
					{Name: "dumb-mcp-backend"},
					{Name: "default-mcp-backend"},
				},
			},
			{
				Name: "yet-another-route",
				Backends: []filterapi.MCPBackend{
					{
						Name: "default-mcp-backend",
						// This shouldn't affect any other routes.
						ToolSelector: &filterapi.MCPToolSelector{Include: []string{"non-existent"}},
					},
					{Name: "dumb-mcp-backend"},
				},
			},
			{
				Name: "awesome-route",
				Backends: []filterapi.MCPBackend{
					{Name: "dumb-mcp-backend"},
				},
			},
		},
	}
	config, err := json.Marshal(filterapi.Config{MCPConfig: mcpConfig, Version: version.Parse()})
	require.NoError(t, err)

	var mcp1, mcp2 *mcp.Server
	env := dataplaneenv.StartTestEnvironment(t,
		func(_ testing.TB, _ io.Writer, ports map[string]int) {
			var srv1, srv2 *http.Server
			srv1, mcp1 = testmcp.NewServer(&testmcp.Options{
				Port:              ports["ts1"],
				ForceJSONResponse: forceJSONResponse,
				DumbEchoServer:    false,
				WriteTimeout:      writeTimeout,
			})
			srv2, mcp2 = testmcp.NewServer(&testmcp.Options{
				Port:              ports["ts2"],
				ForceJSONResponse: forceJSONResponse,
				DumbEchoServer:    true,
				WriteTimeout:      writeTimeout,
			})
			t.Cleanup(func() {
				_ = srv1.Close()
				_ = srv2.Close()
			})
		}, map[string]int{"ts1": 8080, "ts2": 8081, "special_listener": 9999},
		string(config), append(collector.Env(), extraEnv...), envoyConfig, true, true,
		writeTimeout, extprocArgs...,
	)

	m := new(mcpEnv)
	m.mcp1, m.mcp2 = mcp1, mcp2
	m.collector = collector
	m.writeTimeout = writeTimeout
	m.extProcMetricsURL = fmt.Sprintf("http://localhost:%d/metrics", env.ExtProcAdminPort())
	m.baseURL = fmt.Sprintf("http://localhost:%d%s", env.EnvoyListenerPort(), path)
	m.env = env

	m.client = mcp.NewClient(&mcp.Implementation{Name: "demo-http-client", Version: "0.1.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(_ context.Context, request *mcp.ToolListChangedRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received tool list change for session ", request.GetSession().ID(), ": ", request.Params)
				sess.toolListChangedNotifications <- request
			} else {
				t.Fatalf("received tool list change for unknown session ID %q", request.GetSession().ID())
			}
		},
		// TODO: this is due to how the official go-sdk is designed. Notification is a per-session concept but
		// they force the handler to be per-client, which resulted in forcing us to do this multiplexing here.
		ProgressNotificationHandler: func(_ context.Context, request *mcp.ProgressNotificationClientRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received progress notification for session ", request.GetSession().ID(), ": ", request.Params)
				sess.progressNotifications <- request
			} else {
				t.Fatalf("received progress notification for unknown session ID %q", request.GetSession().ID())
			}
		},
		PromptListChangedHandler: func(_ context.Context, request *mcp.PromptListChangedRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received prompt notification for session ", request.GetSession().ID(), ": ", request.Params)
				sess.promptListChangedNotifications <- request
			} else {
				t.Fatalf("received prompt notification for unknown session ID %q", request.GetSession().ID())
			}
		},
		ResourceUpdatedHandler: func(_ context.Context, request *mcp.ResourceUpdatedNotificationRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received resource updated notification for session ", request.GetSession().ID(), ": ", request.Params)
				sess.resourceUpdatedNotifications <- request
			} else {
				t.Fatalf("received resource updated notification for unknown session ID %q", request.GetSession().ID())
			}
		},
		ResourceListChangedHandler: func(_ context.Context, request *mcp.ResourceListChangedRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received resource list changed notification for session ", request.GetSession().ID(), ": ", request.Params)
				sess.resourceListChangedNotifications <- request
			} else {
				t.Fatalf("received resource list changed notification for unknown session ID %q", request.GetSession().ID())
			}
		},

		LoggingMessageHandler: func(_ context.Context, request *mcp.LoggingMessageRequest) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received logging message for session ", request.GetSession().ID(), ": ", request.Params)
				sess.loggingNotification <- request
			} else {
				t.Fatalf("received logging message for unknown session ID %q", request.GetSession().ID())
			}
		},
		CreateMessageHandler: func(_ context.Context, request *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received create message request for session ", request.GetSession().ID(), ": ", request.Params)
				sess.createMessageRequests <- request
			} else {
				t.Fatalf("received create message request for unknown session ID %q", request.GetSession().ID())
			}
			return &mcp.CreateMessageResult{
				Content: &mcp.TextContent{
					Text: "Just plug Envoy into MCP, add some AI magic, and boom — you’ve built yourself an MCP Gateway.",
				},
				Model: "mcp-gatewayinator-3000",
			}, nil
		},
		ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			m.mux.Lock()
			defer m.mux.Unlock()
			if sess, ok := m.sessions[request.GetSession().ID()]; ok {
				t.Log("received elicit request for session ", request.GetSession().ID(), ": ", request.Params)
				sess.elicitRequests <- request
			} else {
				t.Fatalf("received elicit request for unknown session ID %q", request.GetSession().ID())
			}
			return &mcp.ElicitResult{
				Action: "accept",
				Content: map[string]any{
					"name":  "Rob",
					"email": "rob@foo.com",
				},
			}, nil
		},
	})
	m.client.AddRoots(&mcp.Root{
		Name: mcpDefaultRootName,
		URI:  mcpDefaultRootURI,
	})
	return m
}

// newSession creates a new MCP client session and registers it for progress notifications.
func (m *mcpEnv) newSession(t *testing.T) *mcpSession {
	ret := m.newSessionWithoutSpanCheck(t)
	span := m.collector.TakeSpan()
	t.Log("created new MCP session with ID ", ret.session.ID(), ", first span: ", span.String())
	requireMCPSpan(t, span, "Initialize", map[string]string{
		"mcp.method.name":    "initialize",
		"mcp.client.name":    "demo-http-client",
		"mcp.client.title":   "",
		"mcp.client.version": "0.1.0",
	})
	return ret
}

// newSessionWithoutSpanCheck is newSession without the initialize span
// assertion, for callers that assert a different semantic convention.
func (m *mcpEnv) newSessionWithoutSpanCheck(t *testing.T) *mcpSession {
	ret := &mcpSession{
		toolListChangedNotifications:     make(chan *mcp.ToolListChangedRequest, 100),
		progressNotifications:            make(chan *mcp.ProgressNotificationClientRequest, 100),
		promptListChangedNotifications:   make(chan *mcp.PromptListChangedRequest, 100),
		resourceUpdatedNotifications:     make(chan *mcp.ResourceUpdatedNotificationRequest, 100),
		resourceListChangedNotifications: make(chan *mcp.ResourceListChangedRequest, 100),
		loggingNotification:              make(chan *mcp.LoggingMessageRequest, 100),
		createMessageRequests:            make(chan *mcp.CreateMessageRequest, 100),
		elicitRequests:                   make(chan *mcp.ElicitRequest, 100),
	}
	var err error

	ret.session, err = m.client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: m.baseURL}, nil)
	require.NoError(t, err)

	// **NOTE*** Do not add any direct access to the in-memory server session. Otherwise, the tests will result in
	// not being able to run in end-to-end tests. The test code must solely operate through the client side sessions.
	t.Cleanup(func() {
		_ = ret.session.Close()
		m.mux.Lock()
		defer m.mux.Unlock()
		delete(m.sessions, ret.session.ID())
		close(ret.toolListChangedNotifications)
		close(ret.progressNotifications)
		close(ret.promptListChangedNotifications)
		close(ret.resourceUpdatedNotifications)
		close(ret.resourceListChangedNotifications)
		close(ret.loggingNotification)
		close(ret.createMessageRequests)
	})
	m.mux.Lock()
	defer m.mux.Unlock()
	if m.sessions == nil {
		m.sessions = make(map[string]*mcpSession)
	}
	m.sessions[ret.session.ID()] = ret
	return ret
}

// requireMCPSpan verifies that a span has the expected name and attributes.
// It combines base MCP attributes with additional attributes provided by the caller,
// then compares the entire attribute map against the span's attributes.
func requireMCPSpan(t *testing.T, span *tracev1.Span, expectedName string, additionalAttrs map[string]string) {
	t.Helper()
	require.NotNil(t, span, "expected span, actual nil")
	require.Equalf(t, expectedName, span.Name, "span name mismatch, full span: %s", span.String())

	// Extract all attributes from span into map[string]string
	attrsFromSpan := make(map[string]string)
	for _, attr := range span.Attributes {
		if attr.Value.Value != nil {
			if _, ok := attr.Value.Value.(*commonv1.AnyValue_StringValue); ok {
				attrsFromSpan[attr.Key] = attr.Value.GetStringValue()
			}
		}
	}

	// Combine base attributes with additional attributes
	combined := make(map[string]string)
	// Base attributes that are always present
	combined["mcp.protocol.version"] = "2025-06-18"
	combined["mcp.transport"] = "http"
	// mcp.request.id is dynamic, so we copy it from span
	if reqID, ok := attrsFromSpan["mcp.request.id"]; ok {
		combined["mcp.request.id"] = reqID
	}
	// Add additional attributes provided by caller
	maps.Copy(combined, additionalAttrs)

	require.Equalf(t, combined, attrsFromSpan, "span attributes mismatch, full span: %s", span.String())
}

// requireMCPSpanWithException verifies an MCP span with expected attributes and an exception event.
func requireMCPSpanWithException(t *testing.T, span *tracev1.Span, expectedName string, additionalAttrs map[string]string, exceptionMessage string) {
	t.Helper()
	// First verify the basic span attributes
	requireMCPSpan(t, span, expectedName, additionalAttrs)

	// Then verify the exception event
	require.NotEmpty(t, span.Events, "expected span to have exception event")
	foundException := false
	for _, event := range span.Events {
		if event.Name == "exception" {
			foundException = true
			// Check that the exception message contains the expected substring
			for _, attr := range event.Attributes {
				if attr.Key == "exception.message" {
					actualMsg := attr.Value.GetStringValue()
					require.Contains(t, actualMsg, exceptionMessage, "exception message mismatch")
				}
			}
		}
	}
	require.True(t, foundException, "expected span to have exception event but none found")
}

// backendsFromSpan extracts backend names from "route to backend" events in the span.
func backendsFromSpan(t *testing.T, span *tracev1.Span) []string {
	t.Helper()
	// Extract backends from span events
	var backends []string
	for _, event := range span.Events {
		if event.Name == "route to backend" {
			for _, attr := range event.Attributes {
				if attr.Key == "mcp.backend.name" {
					backends = append(backends, attr.Value.GetStringValue())
				}
			}
		}
	}
	return backends
}
