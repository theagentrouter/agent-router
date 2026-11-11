// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package guardrails

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

type azureContentSafetyEvaluator struct {
	config *filterapi.AzureContentSafetyGuardrailProvider
	client *http.Client
}

func newAzureContentSafetyEvaluator(config *filterapi.AzureContentSafetyGuardrailProvider, client *http.Client) (filterapi.GuardrailEvaluator, error) {
	if config == nil || config.Endpoint == "" {
		return nil, fmt.Errorf("azure Content Safety endpoint is required")
	}
	if config.APIKey == "" {
		return nil, fmt.Errorf("azure Content Safety API key is required")
	}
	configCopy := *config
	if configCopy.APIVersion == "" {
		configCopy.APIVersion = "2024-09-01"
	}
	if configCopy.SeverityThreshold == nil {
		threshold := int32(4)
		configCopy.SeverityThreshold = &threshold
	}
	return &azureContentSafetyEvaluator{config: &configCopy, client: client}, nil
}

func (e *azureContentSafetyEvaluator) Evaluate(ctx context.Context, body []byte, _ filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	payload := struct {
		Text string `json:"text"`
	}{Text: string(body)}
	result := struct {
		CategoriesAnalysis []struct {
			Severity int32 `json:"severity"`
		} `json:"categoriesAnalysis"`
	}{}
	endpoint := strings.TrimRight(e.config.Endpoint, "/") + "/contentsafety/text:analyze?api-version=" + url.QueryEscape(e.config.APIVersion)
	if err := doJSON(ctx, e.client, http.MethodPost, endpoint, payload, func(req *http.Request) {
		req.Header.Set("Ocp-Apim-Subscription-Key", e.config.APIKey)
	}, &result); err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("azure Content Safety analyze request failed: %w", err)
	}
	for _, category := range result.CategoriesAnalysis {
		if category.Severity >= *e.config.SeverityThreshold {
			return filterapi.GuardrailEvaluationResult{Matched: true}, nil
		}
	}
	return filterapi.GuardrailEvaluationResult{}, nil
}
