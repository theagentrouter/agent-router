// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

const defaultHTTPGuardrailPath = "/analyze"

// HTTP guardrail contract stages sent in the request context.
const (
	httpGuardrailStageInput  = "input"
	httpGuardrailStageOutput = "output"
)

// HTTP guardrail contract actions returned by the custom service.
const (
	httpGuardrailActionAllow  = "allow"
	httpGuardrailActionBlock  = "block"
	httpGuardrailActionModify = "modify"
)

// httpGuardrailRequest is the payload sent to a custom guardrail service.
type httpGuardrailRequest struct {
	Text    string                      `json:"text"`
	Context httpGuardrailRequestContext `json:"context"`
}

type httpGuardrailRequestContext struct {
	Stage string `json:"stage"`
}

// httpGuardrailResponse is the normalized response returned by a custom guardrail service.
type httpGuardrailResponse struct {
	Action      string                 `json:"action"`
	Findings    []httpGuardrailFinding `json:"findings,omitempty"`
	Replacement *string                `json:"replacement,omitempty"`
}

type httpGuardrailFinding struct {
	Type string `json:"type,omitempty"`
	textSpan
}

type httpEvaluator struct {
	url             string
	apiKey          string
	maskReplacement string
	client          *http.Client
}

func newHTTPEvaluator(config *filterapi.HTTPGuardrailProvider, maskReplacement string, client *http.Client) (filterapi.GuardrailEvaluator, error) {
	if config == nil || config.Endpoint == "" {
		return nil, fmt.Errorf("http guardrail endpoint is required")
	}
	path := config.Path
	if path == "" {
		path = defaultHTTPGuardrailPath
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("http guardrail path must start with /")
	}
	if maskReplacement == "" {
		maskReplacement = "[REDACTED]"
	}
	return &httpEvaluator{
		url:             strings.TrimRight(config.Endpoint, "/") + path,
		apiKey:          config.APIKey,
		maskReplacement: maskReplacement,
		client:          client,
	}, nil
}

func (e *httpEvaluator) Evaluate(ctx context.Context, body []byte, phase filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	stage := httpGuardrailStageInput
	if phase == filterapi.GuardrailPhaseResponse {
		stage = httpGuardrailStageOutput
	}
	payload := httpGuardrailRequest{Text: string(body), Context: httpGuardrailRequestContext{Stage: stage}}

	var result httpGuardrailResponse
	if err := doJSON(ctx, e.client, http.MethodPost, e.url, payload, func(req *http.Request) {
		if e.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+e.apiKey)
		}
	}, &result); err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("http guardrail analyze request failed: %w", err)
	}

	switch strings.ToLower(result.Action) {
	case httpGuardrailActionAllow:
		return filterapi.GuardrailEvaluationResult{}, nil
	case httpGuardrailActionBlock:
		return filterapi.GuardrailEvaluationResult{Matched: true, Replacement: e.maskFindings(body, result.Findings)}, nil
	case httpGuardrailActionModify:
		if result.Replacement != nil {
			return filterapi.GuardrailEvaluationResult{Matched: true, Replacement: []byte(*result.Replacement)}, nil
		}
		if len(result.Findings) == 0 {
			return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("http guardrail returned modify without replacement or findings")
		}
		return filterapi.GuardrailEvaluationResult{Matched: true, Replacement: e.maskFindings(body, result.Findings)}, nil
	default:
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("http guardrail returned unsupported action %q", result.Action)
	}
}

// maskFindings masks the finding spans in body. It returns nil when there are no findings,
// so Mask rules fail instead of forwarding unchanged content.
func (e *httpEvaluator) maskFindings(body []byte, findings []httpGuardrailFinding) []byte {
	if len(findings) == 0 {
		return nil
	}
	spans := make([]textSpan, len(findings))
	for i := range findings {
		spans[i] = findings[i].textSpan
	}
	return maskSpans(body, spans, e.maskReplacement)
}
