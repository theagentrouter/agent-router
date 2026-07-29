// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc/codes"

	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// modelsProcessor implements [Processor] for the `/v1/models` endpoint.
// This processor returns an immediate response with the list of models that are declared in the filter
// configuration.
// Since it returns an immediate response after processing the headers, the rest of the methods of the
// Processor are not implemented. Those should never be called.
type modelsProcessor struct {
	passThroughProcessor
	logger *slog.Logger
	models openai.ModelList
}

var _ Processor = (*modelsProcessor)(nil)

// NewModelsProcessor creates a new processor that returns the list of declared models.
func NewModelsProcessor(config *filterapi.RuntimeConfig, requestHeaders map[string]string, logger *slog.Logger, isUpstreamFilter bool, _ bool) (Processor, error) {
	if isUpstreamFilter {
		return passThroughProcessor{}, nil
	}

	host := requestHost(requestHeaders)
	selectedModels := selectModels(requestHeaders, host, config)

	modelList := openai.ModelList{
		Object: "list",
		Data:   make([]openai.Model, 0, len(selectedModels)),
	}
	for _, m := range selectedModels {
		modelList.Data = append(modelList.Data, openai.Model{
			ID:      m.Name,
			Object:  "model",
			OwnedBy: m.OwnedBy,
			Created: openai.JSONUNIXTime(m.CreatedAt),
		})
	}
	return &modelsProcessor{logger: logger.With("host", host), models: modelList}, nil
}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (m *modelsProcessor) ProcessRequestHeaders(_ context.Context, _ *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	body, err := json.Marshal(m.models)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal body: %w", err)
	}

	headerMutation := &extprocv3.HeaderMutation{}
	setHeader(headerMutation, "content-length", fmt.Sprintf("%d", len(body)))
	setHeader(headerMutation, "content-type", "application/json")

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status:     &typev3.HttpStatus{Code: typev3.StatusCode_OK},
				Headers:    headerMutation,
				Body:       body,
				GrpcStatus: &extprocv3.GrpcStatus{Status: uint32(codes.OK)},
			},
		},
	}, nil
}

func setHeader(headers *extprocv3.HeaderMutation, key, value string) {
	headers.SetHeaders = append(headers.SetHeaders, &corev3.HeaderValueOption{
		Header: &corev3.HeaderValue{
			Key:      key,
			RawValue: []byte(value),
		},
	})
}

// requestHost normalizes the host/authority header for matching (lowercases and strips port).
func requestHost(headers map[string]string) string {
	host := headers[":authority"]
	if host == "" {
		host = headers["host"]
	}
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// selectModels returns the models for the request, applying hostname and header scoping when configured.
func selectModels(requestHeaders map[string]string, host string, cfg *filterapi.RuntimeConfig) []filterapi.Model {
	if len(cfg.ModelsByHostAndScope) == 0 {
		return selectModelsForHost(host, cfg)
	}
	hsm := lookupHeaderScopedModels(host, cfg)
	return selectModelsForHeaderScope(requestHeaders, hsm)
}

// lookupHeaderScopedModels resolves the header-scoped model group for the request host.
func lookupHeaderScopedModels(host string, cfg *filterapi.RuntimeConfig) filterapi.HeaderScopedModels {
	if len(cfg.ModelsByHostAndScope) == 0 {
		return filterapi.HeaderScopedModels{}
	}
	if len(cfg.ModelsByHost) == 0 {
		return cfg.ModelsByHostAndScope[""]
	}
	if hsm, ok := cfg.ModelsByHostAndScope[host]; ok {
		return hsm
	}
	if bestMatch := wildcardHostScopedModels(host, cfg.ModelsByHostAndScope); bestMatch != nil {
		return *bestMatch
	}
	return cfg.ModelsByHostAndScope[""]
}

func wildcardHostScopedModels(host string, modelsByHostAndScope map[string]filterapi.HeaderScopedModels) *filterapi.HeaderScopedModels {
	bestMatchLength := -1
	var bestMatch *filterapi.HeaderScopedModels
	for pattern, hsm := range modelsByHostAndScope {
		if !strings.HasPrefix(pattern, "*.") {
			continue
		}
		suffix := strings.TrimPrefix(pattern, "*.")
		if !strings.HasSuffix(host, "."+suffix) {
			continue
		}
		prefix := strings.TrimSuffix(host, "."+suffix)
		if strings.Contains(prefix, ".") {
			continue
		}
		if len(suffix) > bestMatchLength {
			bestMatchLength = len(suffix)
			match := hsm
			bestMatch = &match
		}
	}
	return bestMatch
}

// selectModelsForHeaderScope returns models for the request's header scope within a host bucket.
func selectModelsForHeaderScope(requestHeaders map[string]string, hsm filterapi.HeaderScopedModels) []filterapi.Model {
	if len(hsm.ModelsByHeaderScope) == 0 {
		return hsm.UnscopedHeaderModels
	}
	scopeKey := filterapi.RequestHeaderScopeKey(hsm.ModelsByHeaderScope, requestHeaders)
	var selected []filterapi.Model
	if scopeKey != "" {
		if models, ok := hsm.ModelsByHeaderScope[scopeKey]; ok {
			selected = append(selected, models...)
		}
	}
	selected = append(selected, hsm.UnscopedHeaderModels...)
	return dedupeModelsByName(selected)
}

func dedupeModelsByName(models []filterapi.Model) []filterapi.Model {
	if len(models) == 0 {
		return models
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]filterapi.Model, 0, len(models))
	for _, model := range models {
		if _, ok := seen[model.Name]; ok {
			continue
		}
		seen[model.Name] = struct{}{}
		result = append(result, model)
	}
	return result
}

// selectModelsForHost returns the models for the given host, falling back to the global list.
func selectModelsForHost(host string, cfg *filterapi.RuntimeConfig) []filterapi.Model {
	if host == "" || len(cfg.ModelsByHost) == 0 {
		return cfg.DeclaredModels
	}

	if exact, ok := cfg.ModelsByHost[host]; ok {
		return exact
	}

	bestMatchLength := -1
	var bestMatchModels []filterapi.Model
	for pattern, models := range cfg.ModelsByHost {
		if !strings.HasPrefix(pattern, "*.") {
			continue
		}
		suffix := strings.TrimPrefix(pattern, "*.")
		// Per the Gateway API spec, a wildcard label matches exactly one DNS label, not multiple.
		// "*.bentoml.com" matches "api.bentoml.com" but NOT "foo.api.bentoml.com", "bentoml.com",
		// or "evilbentoml.com". Verify both the label boundary and that the prefix is a single label.
		if !strings.HasSuffix(host, "."+suffix) {
			continue
		}
		prefix := strings.TrimSuffix(host, "."+suffix)
		if strings.Contains(prefix, ".") {
			continue
		}
		if len(suffix) > bestMatchLength {
			bestMatchLength = len(suffix)
			bestMatchModels = models
		}
	}
	if bestMatchModels != nil {
		return bestMatchModels
	}

	// Unmatched host: fall back to UnscopedModels (models from routes that didn't declare hostnames).
	// We do NOT fall back to DeclaredModels because hostname scoping is opt-in — leaking host-scoped
	// models to unknown hosts would defeat the purpose. UnscopedModels is nil when every route is
	// scoped, which correctly degrades to an empty list.
	return cfg.UnscopedModels
}
