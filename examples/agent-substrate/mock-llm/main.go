// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// mock-llm is a minimal OpenAI-compatible chat completions server for the
// Agent Substrate example. Every response reports the same token usage, so the
// budget math in the walkthrough is predictable.
//
// It follows OpenAI's streaming contract: a streamed response carries a usage
// chunk only when the request sets stream_options.include_usage. Agent Router
// sets that flag itself when token costs are configured, so the actor never
// has to.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type chatRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// reply is the canned assistant message, streamed one word per chunk.
var reply = []string{"Hello", " from", " the", " mock", " LLM."}

func main() {
	u := usage{
		PromptTokens:     envInt("PROMPT_TOKENS", 50),
		CompletionTokens: envInt("COMPLETION_TOKENS", 150),
	}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	// When set, requests must carry this provider key. Agent Router injects it
	// from a BackendSecurityPolicy, so a request that still carries the actor's
	// JWT (or anything else) is refused, the way a real provider would.
	apiKey := os.Getenv("EXPECTED_API_KEY")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		// Say what kind of credential arrived (never its value), so the example
		// can show the provider key replaced the actor's JWT.
		w.Header().Set("x-upstream-auth", describeAuth(r.Header.Get("Authorization"), apiKey))
		if apiKey != "" && r.Header.Get("Authorization") != "Bearer "+apiKey {
			log.Printf("rejected: Authorization is not the provider key")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":{"message":"invalid provider API key","type":"invalid_request_error"}}`)
			return
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		// Echo the actor identity the upstream received, so the example can show
		// which value survived the gateway (a forged header or the JWT subject).
		actor := r.Header.Get("x-actor-id")
		w.Header().Set("x-upstream-saw-actor-id", actor)
		log.Printf("model=%q stream=%t include_usage=%t x-actor-id=%q", req.Model, req.Stream, includeUsage, actor)

		id := fmt.Sprintf("chatcmpl-mock-%d", time.Now().UnixNano())
		if !req.Stream {
			writeJSON(w, map[string]any{
				"id":      id,
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   req.Model,
				"choices": []map[string]any{{
					"index":         0,
					"message":       map[string]string{"role": "assistant", "content": joinReply()},
					"finish_reason": "stop",
				}},
				"usage": u,
			})
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		chunk := func(choices []map[string]any, u *usage) {
			c := map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   req.Model,
				"choices": choices,
			}
			if u != nil {
				c["usage"] = u
			}
			b, _ := json.Marshal(c)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		chunk([]map[string]any{{"index": 0, "delta": map[string]string{"role": "assistant", "content": ""}}}, nil)
		for _, word := range reply {
			chunk([]map[string]any{{"index": 0, "delta": map[string]string{"content": word}}}, nil)
		}
		chunk([]map[string]any{{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}, nil)
		if includeUsage {
			chunk([]map[string]any{}, &u)
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	addr := ":" + strconv.Itoa(envInt("PORT", 8080))
	log.Printf("mock-llm listening on %s, usage per response: %+v", addr, u)
	log.Fatal(http.ListenAndServe(addr, mux)) //nolint:gosec // demo server, no timeouts needed
}

func describeAuth(auth, apiKey string) string {
	switch {
	case auth == "":
		return "none"
	case apiKey != "" && auth == "Bearer "+apiKey:
		return "provider-key"
	case strings.HasPrefix(auth, "Bearer eyJ"):
		return "jwt"
	default:
		return "other"
	}
}

func joinReply() (s string) {
	for _, w := range reply {
		s += w
	}
	return
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Fatalf("%s must be a non-negative integer, got %q", name, v)
		}
		return n
	}
	return def
}
