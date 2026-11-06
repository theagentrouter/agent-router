// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// actor is a tiny stand-in for an agent running on Agent Substrate. It holds
// no credentials: every call to Agent Router carries a placeholder bearer
// token, which Substrate's egress gateway replaces with a JWT naming this
// actor.
//
// Endpoints (reached through `kubectl ate proxy actor`):
//
//	POST /        {"model","prompt","stream","headers","url"} -> one chat completion
//	POST /bench   {"n","model","url"}                         -> sequential latency run
//	GET  /lookup?host=<name>                                  -> DNS lookup from inside the sandbox
//	GET  /readyz                                              -> wakeup probe
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

var (
	defaultURL = env("AGENT_ROUTER_URL", "http://agent-router.envoy-gateway-system.svc.cluster.local/v1/chat/completions")
	client     = &http.Client{Timeout: 30 * time.Second}
	// echoHeaders are the response headers worth showing: the rate limit
	// verdict and the identity the upstream model server received.
	echoHeaders = []string{"x-ratelimit-limit", "x-ratelimit-remaining", "x-ratelimit-reset", "x-envoy-ratelimited", "x-upstream-saw-actor-id", "x-upstream-auth"}
)

type callRequest struct {
	URL     string            `json:"url"`
	Model   string            `json:"model"`
	Prompt  string            `json:"prompt"`
	Stream  bool              `json:"stream"`
	Headers map[string]string `json:"headers"` // extra request headers, e.g. a forged x-actor-id
}

type callResult struct {
	Status    int               `json:"status"` // HTTP status from Agent Router; 0 if the request never got a response
	Error     string            `json:"error,omitempty"`
	LatencyMS float64           `json:"latency_ms"`
	Headers   map[string]string `json:"headers,omitempty"`
	Usage     json.RawMessage   `json:"usage,omitempty"`
	Content   string            `json:"content,omitempty"`
	Chunks    int               `json:"chunks,omitempty"` // SSE data events received, streamed calls only
	Body      json.RawMessage   `json:"body,omitempty"`   // raw body for non-streamed calls
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		var req callRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, call(r.Context(), req))
	})
	mux.HandleFunc("POST /bench", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			N int `json:"n"`
			callRequest
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.N <= 0 {
			http.Error(w, `want {"n": <positive int>, ...}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, bench(r.Context(), req.N, req.callRequest))
	})
	mux.HandleFunc("GET /lookup", func(w http.ResponseWriter, r *http.Request) {
		host := r.URL.Query().Get("host")
		addrs, err := net.DefaultResolver.LookupHost(r.Context(), host)
		out := map[string]any{"host": host, "addrs": addrs}
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			out["error"], out["not_found"] = dnsErr.Error(), dnsErr.IsNotFound
		} else if err != nil {
			out["error"] = err.Error()
		}
		writeJSON(w, out)
	})
	addr := ":" + env("PORT", "80")
	log.Printf("actor listening on %s, Agent Router at %s", addr, defaultURL)
	log.Fatal(http.ListenAndServe(addr, mux)) //nolint:gosec // demo server, no timeouts needed
}

func call(ctx context.Context, req callRequest) (res callResult) {
	url := cmp.Or(req.URL, defaultURL)
	body, _ := json.Marshal(map[string]any{
		"model":    cmp.Or(req.Model, "demo-model"),
		"messages": []map[string]string{{"role": "user", "content": cmp.Or(req.Prompt, "Say hello.")}},
		"stream":   req.Stream,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return callResult{Error: err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// The actor has no real credential. Substrate's egress gateway replaces
	// this header with an actor JWT, but only if the header is present.
	httpReq.Header.Set("Authorization", "Bearer placeholder")
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	defer func() { res.LatencyMS = float64(time.Since(start).Microseconds()) / 1000 }()
	resp, err := client.Do(httpReq)
	if err != nil {
		return callResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode
	res.Headers = map[string]string{}
	for _, h := range echoHeaders {
		if v := resp.Header.Get(h); v != "" {
			res.Headers[h] = v
		}
	}

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			res.Error = err.Error()
		}
		var parsed struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(b, &parsed) == nil {
			res.Usage, res.Body = parsed.Usage, b
		} else {
			res.Body, _ = json.Marshal(string(b))
		}
		return res
	}

	// Streamed response: collect the content and the usage chunk, if any.
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		res.Chunks++
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			res.Content += c.Delta.Content
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			res.Usage = chunk.Usage
		}
	}
	if err := sc.Err(); err != nil {
		res.Error = err.Error()
	}
	return res
}

// bench makes n sequential calls and reports latency percentiles measured
// inside this process, so the numbers exclude the ingress path used to reach
// the actor.
func bench(ctx context.Context, n int, req callRequest) map[string]any {
	lat := make([]float64, 0, n)
	statuses := map[string]int{}
	for range n {
		r := call(ctx, req)
		statuses[fmt.Sprint(r.Status)]++
		if r.Status == http.StatusOK {
			lat = append(lat, r.LatencyMS)
		}
	}
	slices.Sort(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(p*float64(len(lat))))]
	}
	return map[string]any{
		"url": cmp.Or(req.URL, defaultURL), "n": n, "statuses": statuses,
		"p50_ms": pct(0.50), "p95_ms": pct(0.95), "p99_ms": pct(0.99),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func env(name, def string) string { return cmp.Or(os.Getenv(name), def) }
