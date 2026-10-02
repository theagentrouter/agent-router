// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package guardrails implements external content-safety provider adapters.
package guardrails

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

const defaultTimeoutSeconds int32 = 10

// NewEvaluator creates an evaluator for the configured external provider.
func NewEvaluator(ctx context.Context, provider *filterapi.GuardrailProvider) (filterapi.GuardrailEvaluator, error) {
	client := &http.Client{Timeout: time.Duration(timeoutSeconds(provider.TimeoutSeconds)) * time.Second}
	switch provider.Type {
	case filterapi.GuardrailProviderTypePresidio:
		return newPresidioEvaluator(provider.Presidio, provider.MaskReplacement, client)
	case filterapi.GuardrailProviderTypeBedrockGuardrails:
		return newBedrockEvaluator(ctx, provider.Bedrock, client)
	case filterapi.GuardrailProviderTypeAzureContentSafety:
		return newAzureContentSafetyEvaluator(provider.AzureContentSafety, client)
	case filterapi.GuardrailProviderTypeHTTP:
		return newHTTPEvaluator(provider.HTTP, provider.MaskReplacement, client)
	default:
		return nil, fmt.Errorf("unsupported external guardrail provider %q", provider.Type)
	}
}

func timeoutSeconds(configured int32) int32 {
	if configured <= 0 {
		return defaultTimeoutSeconds
	}
	return configured
}

func doJSON(ctx context.Context, client *http.Client, method, endpoint string, body any, mutate func(*http.Request), response any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	return sendJSON(req, client, response)
}

func sendJSON(req *http.Request, client *http.Client, response any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("provider returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err = json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("cannot decode provider response: %w", err)
	}
	return nil
}

// textSpan is a detected region of evaluated text. Offsets are Unicode code point
// positions and End is exclusive.
type textSpan struct {
	Start int     `json:"start"`
	End   int     `json:"end"`
	Score float64 `json:"score"`
}

// maskSpans replaces each valid span in body with replacement. Invalid spans are
// ignored and overlapping or adjacent spans are merged into a single replacement.
// It returns nil when no span is valid so Mask rules never forward unmasked content.
func maskSpans(body []byte, spans []textSpan, replacement string) []byte {
	runes := []rune(string(body))
	valid := make([]textSpan, 0, len(spans))
	for _, span := range spans {
		if span.Start < 0 || span.End > len(runes) || span.Start >= span.End {
			continue
		}
		valid = append(valid, span)
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Start < valid[j].Start })
	merged := valid[:0]
	for _, span := range valid {
		if n := len(merged); n > 0 && span.Start <= merged[n-1].End {
			merged[n-1].End = max(merged[n-1].End, span.End)
			continue
		}
		merged = append(merged, span)
	}
	for i := len(merged) - 1; i >= 0; i-- {
		span := merged[i]
		runes = append(runes[:span.Start], append([]rune(replacement), runes[span.End:]...)...)
	}
	return []byte(string(runes))
}
