// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testmcp

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

const (
	modernProtocolVersion = "2026-07-28"
	mcpMethodHeader       = "Mcp-Method"
	mcpProtocolVerHeader  = "Mcp-Protocol-Version"
	mcpNameHeader         = "Mcp-Name"
)

// ModernOptions configures the modern MCP test server.
type ModernOptions struct {
	Port           int
	WriteTimeout   time.Duration
	DumbEchoServer bool
}

// jsonRPCRequest is a minimal JSON-RPC request envelope.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// jsonRPCResponse is a minimal JSON-RPC success response envelope.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

// jsonRPCError is a JSON-RPC error response envelope.
type jsonRPCError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// modernToolDef is a serializable tool definition.
type modernToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// NewModernServer starts a modern (2026-07-28) MCP test server.
// It responds to stateless POST requests with JSON-RPC over plain JSON.
func NewModernServer(opts *ModernOptions) *http.Server {
	var handler http.Handler
	if opts.DumbEchoServer {
		handler = newModernDumbHandler()
	} else {
		handler = newModernFullHandler()
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", opts.Port),
		ReadHeaderTimeout: 3 * time.Second,
		WriteTimeout:      opts.WriteTimeout,
		Handler:           handler,
		ConnState: func(conn net.Conn, state http.ConnState) {
			log.Printf("MODERN MCP SERVER connection [%s] %s -> %s\n", state, conn.RemoteAddr(), conn.LocalAddr())
		},
	}
	go func() {
		log.Printf("starting Modern MCP Stateless-HTTP server on :%d at /mcp", opts.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("modern server error: %v", err)
		}
	}()
	return server
}

func newModernDumbHandler() http.Handler {
	tools := []modernToolDef{
		{Name: ToolDumbEcho.Tool.Name, Description: ToolDumbEcho.Tool.Description, InputSchema: ToolDumbEcho.Tool.InputSchema},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body error", http.StatusBadRequest)
			return
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		method := r.Header.Get(mcpMethodHeader)
		switch method {
		case "server/discover":
			writeJSONRPC(w, req.ID, map[string]any{
				"supportedVersions": []string{modernProtocolVersion},
				"capabilities":      map[string]any{"tools": map[string]any{"listChanged": true}},
			})
		case "tools/list":
			writeJSONRPC(w, req.ID, map[string]any{"tools": tools})
		case "tools/call":
			name := r.Header.Get(mcpNameHeader)
			if name == ToolDumbEcho.Tool.Name {
				var args ToolEchoArgs
				extractArgs(req.Params, &args)
				writeJSONRPC(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "dumb echo: " + args.Text}},
				})
			} else {
				writeJSONRPCError(w, req.ID, -32601, "tool not found: "+name)
			}
		default:
			writeJSONRPCError(w, req.ID, -32601, "method not found: "+method)
		}
	})
}

func newModernFullHandler() http.Handler {
	allTools := []modernToolDef{
		{Name: ToolEcho.Tool.Name, Description: ToolEcho.Tool.Description, InputSchema: ToolEcho.Tool.InputSchema},
		{Name: ToolSum.Tool.Name, Description: ToolSum.Tool.Description, InputSchema: ToolSum.Tool.InputSchema},
		{Name: ToolError.Tool.Name, Description: ToolError.Tool.Description, InputSchema: ToolError.Tool.InputSchema},
	}
	allPrompts := []map[string]any{
		{"name": CodeReviewPrompt.Name, "description": CodeReviewPrompt.Description, "arguments": CodeReviewPrompt.Arguments},
	}
	allResources := []map[string]any{
		{"name": DummyResource.Name, "uri": DummyResource.URI, "mimeType": DummyResource.MIMEType},
		{"name": UIRendererResource.Name, "uri": UIRendererResource.URI, "mimeType": UIRendererResource.MIMEType},
	}
	allResourceTemplates := []map[string]any{
		{"name": DummyResourceTemplate.Name, "uriTemplate": DummyResourceTemplate.URITemplate, "description": DummyResourceTemplate.Description, "mimeType": DummyResourceTemplate.MIMEType},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body error", http.StatusBadRequest)
			return
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		method := r.Header.Get(mcpMethodHeader)
		switch method {
		case "server/discover":
			writeJSONRPC(w, req.ID, map[string]any{
				"supportedVersions": []string{modernProtocolVersion},
				"capabilities": map[string]any{
					"tools":     map[string]any{"listChanged": true},
					"resources": map[string]any{"subscribe": true, "listChanged": true},
					"prompts":   map[string]any{"listChanged": true},
					"logging":   map[string]any{},
				},
			})
		case "tools/list":
			writeJSONRPC(w, req.ID, map[string]any{"tools": allTools})
		case "tools/call":
			handleModernToolCall(w, r, &req)
		case "resources/list":
			writeJSONRPC(w, req.ID, map[string]any{"resources": allResources})
		case "resources/read":
			handleModernResourceRead(w, &req)
		case "resources/templates/list":
			writeJSONRPC(w, req.ID, map[string]any{"resourceTemplates": allResourceTemplates})
		case "prompts/list":
			writeJSONRPC(w, req.ID, map[string]any{"prompts": allPrompts})
		case "prompts/get":
			handleModernPromptGet(w, &req)
		case "completion/complete":
			handleModernComplete(w, &req)
		default:
			writeJSONRPCError(w, req.ID, -32601, "method not found: "+method)
		}
	})
}

func handleModernToolCall(w http.ResponseWriter, r *http.Request, req *jsonRPCRequest) {
	name := r.Header.Get(mcpNameHeader)
	switch name {
	case ToolEcho.Tool.Name:
		var args ToolEchoArgs
		extractArgs(req.Params, &args)
		writeJSONRPC(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": args.Text}},
		})
	case ToolSum.Tool.Name:
		var args ToolSumArgs
		extractArgs(req.Params, &args)
		writeJSONRPC(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("%g", args.A+args.B)}},
		})
	case ToolError.Tool.Name:
		var args ToolErrorArgs
		extractArgs(req.Params, &args)
		writeJSONRPC(w, req.ID, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": args.Error}},
		})
	default:
		writeJSONRPCError(w, req.ID, -32601, "tool not found: "+name)
	}
}

func handleModernResourceRead(w http.ResponseWriter, req *jsonRPCRequest) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	switch params.URI {
	case DummyResource.URI:
		writeJSONRPC(w, req.ID, map[string]any{
			"contents": []map[string]any{{"uri": DummyResource.URI, "mimeType": DummyResource.MIMEType, "blob": "dummy"}},
		})
	case UIRendererResource.URI:
		writeJSONRPC(w, req.ID, map[string]any{
			"contents": []map[string]any{{"uri": UIRendererResource.URI, "mimeType": UIRendererResource.MIMEType, "blob": "<html>renderer</html>"}},
		})
	default:
		writeJSONRPCError(w, req.ID, -32002, fmt.Sprintf("Resource not found: %s", params.URI))
	}
}

func handleModernPromptGet(w http.ResponseWriter, req *jsonRPCRequest) {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	if params.Name == CodeReviewPrompt.Name {
		writeJSONRPC(w, req.ID, map[string]any{
			"description": "Code review prompt",
			"messages": []map[string]any{
				{"role": "user", "content": map[string]any{"type": "text", "text": "Please review the following code: " + params.Arguments["Code"]}},
			},
		})
	} else {
		writeJSONRPCError(w, req.ID, -32602, "prompt not found: "+params.Name)
	}
}

func handleModernComplete(w http.ResponseWriter, req *jsonRPCRequest) {
	var params struct {
		Ref struct {
			Type string `json:"type"`
			Name string `json:"name"`
			URI  string `json:"uri"`
		} `json:"ref"`
		Argument struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"argument"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	if params.Ref.Name != "" {
		writeJSONRPC(w, req.ID, map[string]any{
			"completion": map[string]any{"values": []string{"python", "pytorch", "pyside"}},
		})
	} else {
		updatedURI := strings.Replace(params.Ref.URI, "{id}", params.Argument.Value, 1)
		writeJSONRPC(w, req.ID, map[string]any{
			"completion": map[string]any{"values": []string{updatedURI}},
		})
	}
}

// --- helpers ---

func writeJSONRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	resp := jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result}
	encoded, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	resp := jsonRPCError{JSONRPC: "2.0", ID: id}
	resp.Error.Code = code
	resp.Error.Message = msg
	encoded, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "marshal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func extractArgs(params json.RawMessage, dst any) {
	// Extract arguments from the "arguments" field of the params, or fallback to params directly.
	var envelope struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(params, &envelope) == nil && len(envelope.Arguments) > 0 {
		_ = json.Unmarshal(envelope.Arguments, dst)
	} else {
		_ = json.Unmarshal(params, dst)
	}
}
