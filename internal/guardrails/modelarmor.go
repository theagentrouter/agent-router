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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/gcpauth"
)

const modelArmorScope = "https://www.googleapis.com/auth/cloud-platform"

// Model Armor sanitization result values.
const (
	modelArmorMatchFound       = "MATCH_FOUND"
	modelArmorInvocationFailed = "FAILURE"
)

type modelArmorEvaluator struct {
	templateURL string
	client      *http.Client
	tokenSource oauth2.TokenSource
}

func newModelArmorEvaluator(ctx context.Context, config *filterapi.ModelArmorGuardrailProvider, client *http.Client) (filterapi.GuardrailEvaluator, error) {
	if config == nil || config.Project == "" || config.Location == "" || config.Template == "" {
		return nil, fmt.Errorf("model Armor project, location, and template are required")
	}
	tokenSource, err := loadGCPTokenSource(ctx, config, client)
	if err != nil {
		return nil, err
	}
	endpoint := config.Endpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://modelarmor.%s.rep.googleapis.com", config.Location)
	}
	templateURL := strings.TrimRight(endpoint, "/") + "/v1/projects/" + url.PathEscape(config.Project) +
		"/locations/" + url.PathEscape(config.Location) + "/templates/" + url.PathEscape(config.Template)
	return &modelArmorEvaluator{templateURL: templateURL, client: client, tokenSource: tokenSource}, nil
}

// loadGCPTokenSource returns an auto-refreshing token source from the configured service account key,
// or from Application Default Credentials (e.g. GKE Workload Identity) when no key is configured.
func loadGCPTokenSource(ctx context.Context, config *filterapi.ModelArmorGuardrailProvider, client *http.Client) (oauth2.TokenSource, error) {
	transport, err := gcpauth.NewTransport()
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: transport, Timeout: client.Timeout})
	var credentials *google.Credentials
	if config.CredentialsJSON != "" {
		credentials, err = google.CredentialsFromJSONWithType(ctx, []byte(config.CredentialsJSON), google.ServiceAccount, modelArmorScope)
	} else {
		credentials, err = google.FindDefaultCredentials(ctx, modelArmorScope)
	}
	if err != nil {
		return nil, fmt.Errorf("cannot load GCP credentials: %w", err)
	}
	return credentials.TokenSource, nil
}

// modelArmorMatch is the common shape of every Model Armor filter result.
type modelArmorMatch struct {
	MatchState string `json:"matchState"`
}

// modelArmorFilterResult holds the result of one filter. At most one field is set.
type modelArmorFilterResult struct {
	RaiFilterResult            *modelArmorMatch `json:"raiFilterResult,omitempty"`
	PiAndJailbreakFilterResult *modelArmorMatch `json:"piAndJailbreakFilterResult,omitempty"`
	MaliciousURIFilterResult   *modelArmorMatch `json:"maliciousUriFilterResult,omitempty"`
	CsamFilterFilterResult     *modelArmorMatch `json:"csamFilterFilterResult,omitempty"`
	VirusScanFilterResult      *modelArmorMatch `json:"virusScanFilterResult,omitempty"`
	SdpFilterResult            *struct {
		InspectResult    *modelArmorMatch `json:"inspectResult,omitempty"`
		DeidentifyResult *struct {
			MatchState string `json:"matchState"`
			Data       *struct {
				Text string `json:"text"`
			} `json:"data,omitempty"`
		} `json:"deidentifyResult,omitempty"`
	} `json:"sdpFilterResult,omitempty"`
}

type modelArmorResponse struct {
	SanitizationResult struct {
		FilterMatchState string                            `json:"filterMatchState"`
		InvocationResult string                            `json:"invocationResult"`
		FilterResults    map[string]modelArmorFilterResult `json:"filterResults"`
	} `json:"sanitizationResult"`
}

func (e *modelArmorEvaluator) Evaluate(ctx context.Context, body []byte, phase filterapi.GuardrailPhase) (filterapi.GuardrailEvaluationResult, error) {
	method := "sanitizeUserPrompt"
	var payload any = struct {
		UserPromptData struct {
			Text string `json:"text"`
		} `json:"userPromptData"`
	}{UserPromptData: struct {
		Text string `json:"text"`
	}{Text: string(body)}}
	if phase == filterapi.GuardrailPhaseResponse {
		method = "sanitizeModelResponse"
		payload = struct {
			ModelResponseData struct {
				Text string `json:"text"`
			} `json:"modelResponseData"`
		}{ModelResponseData: struct {
			Text string `json:"text"`
		}{Text: string(body)}}
	}
	token, err := e.tokenSource.Token()
	if err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("cannot retrieve GCP access token: %w", err)
	}

	var result modelArmorResponse
	if err = doJSON(ctx, e.client, http.MethodPost, e.templateURL+":"+method, payload, func(req *http.Request) {
		token.SetAuthHeader(req)
	}, &result); err != nil {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("model Armor %s request failed: %w", method, err)
	}
	if result.SanitizationResult.InvocationResult == modelArmorInvocationFailed {
		return filterapi.GuardrailEvaluationResult{}, fmt.Errorf("model Armor %s invocation failed", method)
	}
	if result.SanitizationResult.FilterMatchState != modelArmorMatchFound {
		return filterapi.GuardrailEvaluationResult{}, nil
	}
	return filterapi.GuardrailEvaluationResult{
		Matched:     true,
		Replacement: modelArmorDeidentifiedText(result.SanitizationResult.FilterResults),
	}, nil
}

// modelArmorDeidentifiedText returns the Sensitive Data Protection de-identified text when it is the
// only filter that matched. It returns nil when any other filter matched, so Mask rules never forward
// content that was flagged for a reason masking cannot fix (e.g. a jailbreak attempt).
func modelArmorDeidentifiedText(results map[string]modelArmorFilterResult) []byte {
	var replacement []byte
	for _, result := range results {
		for _, match := range []*modelArmorMatch{
			result.RaiFilterResult, result.PiAndJailbreakFilterResult, result.MaliciousURIFilterResult,
			result.CsamFilterFilterResult, result.VirusScanFilterResult,
		} {
			if match != nil && match.MatchState == modelArmorMatchFound {
				return nil
			}
		}
		if sdp := result.SdpFilterResult; sdp != nil {
			if sdp.InspectResult != nil && sdp.InspectResult.MatchState == modelArmorMatchFound {
				return nil
			}
			if d := sdp.DeidentifyResult; d != nil && d.MatchState == modelArmorMatchFound && d.Data != nil {
				replacement = []byte(d.Data.Text)
			}
		}
	}
	return replacement
}
