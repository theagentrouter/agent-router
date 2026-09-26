// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testmcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

var logger = log.New(os.Stdout, "[mcptestserver] ", 0)

type Options struct {
	Port                              int
	ForceJSONResponse, DumbEchoServer bool
	WriteTimeout                      time.Duration
	DisableLog                        bool
}

// NewServer starts a demo MCP server with two tools: echo and sum.
//
// When forceJSONResponse true, the server will respond with JSON responses
// instead of using text/even-stream. The spec allows both so it is useful
// for us to test both scenarios.
//
// When dumbEchoServer is true, the server will only implement the echo tool,
// and will not implement any prompts or resources. This is useful for testing
// basic routing.
func NewServer(opts *Options) (*http.Server, *mcp.Server) {
	if opts.DumbEchoServer {
		return newDumbServer(opts.Port)
	}

	// --- MCP server implementation.
	handlerCounts := &notificationCounts{}
	s := mcp.NewServer(
		&mcp.Implementation{Name: "demo-http-server", Version: "0.1.0"},
		&mcp.ServerOptions{
			HasTools: true,
			RootsListChangedHandler: func(_ context.Context, request *mcp.RootsListChangedRequest) {
				logger.Printf("RootsListChanged request: %+v", request)
				handlerCounts.RootsListChanged.Add(1)
			},
			SubscribeHandler: func(_ context.Context, request *mcp.SubscribeRequest) error {
				logger.Printf("Subscribe request: %+v", request)
				handlerCounts.Subscribe.Add(1)
				return nil
			},
			UnsubscribeHandler: func(_ context.Context, request *mcp.UnsubscribeRequest) error {
				logger.Printf("Unsubscribe request: %+v", request)
				handlerCounts.Unsubscribe.Add(1)
				return nil
			},
			CompletionHandler: func(_ context.Context, request *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
				logger.Printf("Complete request: %+v", request)
				if request.Params.Ref.Name != "" {
					return &mcp.CompleteResult{
						Completion: mcp.CompletionResultDetails{
							Values: []string{"python", "pytorch", "pyside"},
						},
					}, nil
				}

				updatedURI := strings.Replace(request.Params.Ref.URI, "{id}", request.Params.Argument.Value, 1)
				return &mcp.CompleteResult{
					Completion: mcp.CompletionResultDetails{
						Values: []string{updatedURI},
					},
				}, nil
			},
		},
	)

	// Reject server/discover at the MCP method layer so go-sdk v1.7+ Connect
	// falls back to the legacy initialize handshake. HTTP-level rejection alone
	// is not enough because the Streamable handler may route discover before our
	// wrapper sees modern headers consistently.
	s.AddReceivingMiddleware(func(handler mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "server/discover" {
				return nil, fmt.Errorf("server/discover is not supported by legacy testmcp server")
			}
			return handler(ctx, method, req)
		}
	})

	// Setup API key auth when environment variable TEST_API_KEY is set.
	apiKey := os.Getenv("TEST_API_KEY")
	apiKeyQueryParam := os.Getenv("TEST_API_KEY_QUERY_PARAM")
	// Query param auth takes precedence over header.
	if apiKey != "" && apiKeyQueryParam == "" {
		header := strings.ToLower(cmp.Or(os.Getenv("TEST_API_KEY_HEADER"), "Authorization"))
		expectedValue := apiKey
		if header == "authorization" {
			expectedValue = "Bearer " + apiKey
		}
		s.AddReceivingMiddleware(func(handler mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
				if req.GetExtra().Header.Get(header) != expectedValue {
					return nil, fmt.Errorf("invalid API key")
				}
				return handler(ctx, method, req)
			}
		})
	}

	// Setup claim header validation when environment variable TEST_EXPECTED_CLAIM_HEADERS is set.
	// Format: "header1=value1,header2=value2"
	expectedClaimHeaders := os.Getenv("TEST_EXPECTED_CLAIM_HEADERS")
	if expectedClaimHeaders != "" {
		expected := map[string]string{}
		for _, pair := range strings.Split(expectedClaimHeaders, ",") {
			k, v, ok := strings.Cut(pair, "=")
			if ok {
				expected[k] = v
			}
		}
		s.AddReceivingMiddleware(func(handler mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				for header, value := range expected {
					if got := req.GetExtra().Header.Get(header); got != value {
						return nil, fmt.Errorf("expected header %q=%q, got %q", header, value, got)
					}
				}
				return handler(ctx, method, req)
			}
		})
	}

	s.AddPrompt(CodeReviewPrompt, codReviewPromptHandler)
	s.AddResource(DummyResource, DummyResourceHandler())
	s.AddResource(UIRendererResource, DummyResourceHandler())
	s.AddResourceTemplate(DummyResourceTemplate, DummyResourceHandler())
	mcp.AddTool(s, ToolEcho.Tool, ToolEcho.Handler)
	mcp.AddTool(s, ToolSum.Tool, ToolSum.Handler)
	mcp.AddTool(s, ToolError.Tool, ToolError.Handler)
	mcp.AddTool(s, ToolCountDown.Tool, ToolCountDown.Handler)
	mcp.AddTool(s, ToolContainsRootTool.Tool, ToolContainsRootTool.Handler)
	mcp.AddTool(s, ToolDelay.Tool, ToolDelay.Handler)
	mcp.AddTool(s, ToolElicitEmail.Tool, ToolElicitEmail.Handler)
	mcp.AddTool(s, ToolCreateMessage.Tool, ToolCreateMessage.Handler)
	promptAddTool := newToolAddPrompt(s)
	mcp.AddTool(s, promptAddTool.Tool, promptAddTool.Handler)
	resourceUpdateNotificationTool := newToolResourceUpdateNotification(s)
	mcp.AddTool(s, resourceUpdateNotificationTool.Tool, resourceUpdateNotificationTool.Handler)
	addOrDeleteResourceTool := newToolAddOrDeleteAnotherDummyResource(s)
	mcp.AddTool(s, addOrDeleteResourceTool.Tool, addOrDeleteResourceTool.Handler)
	notificationsCounts := newToolNotificationCounts(handlerCounts)
	mcp.AddTool(s, notificationsCounts.Tool, notificationsCounts.Handler)
	mcp.AddTool(s, ToolUIResource.Tool, ToolUIResource.Handler)
	mcp.AddTool(s, ToolResourceLink.Tool, ToolResourceLink.Handler)
	mcp.AddTool(s, ToolEmbeddedResource.Tool, ToolEmbeddedResource.Handler)

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		// Check for API key in query param if configured.
		if apiKey != "" && apiKeyQueryParam != "" {
			log.Printf("checking for API key in query param %q\n", apiKeyQueryParam)
			queryParam := r.URL.Query().Get(apiKeyQueryParam)
			if queryParam != apiKey {
				// Returning nil will cause 400 response in the current implementation of NewStreamableHTTPHandler.
				log.Printf("invalid API key in query param %q: %q\n", apiKeyQueryParam, queryParam)
				return nil
			}
			log.Printf("valid API key in query param %q\n", apiKeyQueryParam)
		}
		return s
	}, &mcp.StreamableHTTPOptions{JSONResponse: opts.ForceJSONResponse})

	// Reject modern (2026-07-28) requests so go-sdk v1.7+ Connect falls back from
	// server/discover to the legacy initialize handshake. Without this, dataplane
	// legacy tests would silently exercise the modern path.
	legacyOnly := rejectModernProtocol(handler)

	// --- Streamable HTTP transport (default endpoint path is "/mcp").
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.Port),
		ReadHeaderTimeout: 3 * time.Second,
		// Allow long-lived connections.
		WriteTimeout: opts.WriteTimeout,
		Handler:      legacyOnly,
		ConnState: func(conn net.Conn, state http.ConnState) {
			if opts.DisableLog {
				return
			}
			log.Printf("MCP SERVER connection [%s] %s -> %s\n", state, conn.RemoteAddr(), conn.LocalAddr())
		},
	}
	go func() {
		log.Printf("starting MCP Streamable-HTTP server on :%d at /mcp", opts.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Do not Fatalf: that aborts the entire test process and masks the
			// real failure behind an opaque package-level FAIL.
			log.Printf("server error: %v", err)
		}
	}()
	return server, s
}

func newDumbServer(port int) (*http.Server, *mcp.Server) {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "dumb-echo-server", Version: "0.1.0"},
		&mcp.ServerOptions{
			// Explicitly set empty capabilities so the server does NOT advertise logging support.
			// The default (nil) would advertise logging for historical reasons in the SDK.
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{ListChanged: true},
			},
		},
	)

	// Reject server/discover and logging/setLevel so legacy dataplane tests stay
	// on the initialize path and never forward unsupported methods to this server.
	s.AddReceivingMiddleware(func(handler mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "logging/setLevel" {
				return nil, fmt.Errorf("logging/setLevel is not supported by dumb-echo-server")
			}
			if method == "server/discover" {
				return nil, fmt.Errorf("server/discover is not supported by dumb-echo-server")
			}
			return handler(ctx, method, req)
		}
	})

	mcp.AddTool(s, ToolDumbEcho.Tool, ToolDumbEcho.Handler)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{})
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), ReadHeaderTimeout: 3 * time.Second, Handler: rejectModernProtocol(handler)}
	go func() {
		log.Printf("starting DUMB MCP Streamable-HTTP server on :%d at /mcp", port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server error: %v", err)
		}
	}()
	return server, s
}

// rejectModernProtocol wraps a legacy Streamable HTTP handler so that modern
// (2026-07-28) requests — notably server/discover from go-sdk v1.7+ Connect —
// are rejected. That forces the client to fall back to the initialize handshake,
// keeping dataplane legacy tests on the legacy path.
//
// The JSON-RPC error MUST echo the request id. A null id makes go-sdk treat the
// response as invalid and close the connection, so Connect never reaches
// initialize (breaks BenchmarkMCP/Baseline_NoProxy).
func rejectModernProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := r.Header.Get("Mcp-Protocol-Version")
		method := r.Header.Get("Mcp-Method")
		if version == "2026-07-28" || method == "server/discover" {
			id := json.RawMessage("null")
			if body, err := io.ReadAll(r.Body); err == nil {
				var req struct {
					ID json.RawMessage `json:"id"`
				}
				if json.Unmarshal(body, &req) == nil && len(req.ID) > 0 {
					id = req.ID
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found: server/discover"}}`, id)
			return
		}
		next.ServeHTTP(w, r)
	})
}
