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
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc/codes"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
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
	selectedModels := selectModelsForHost(host, config)

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

// anthropicModelsProcessor implements [Processor] for the Anthropic-compatible `/v1/models` endpoint.
// This processor returns an immediate response with the list of models that are declared in the filter
// configuration, formatted as an Anthropic model list rather than an OpenAI one.
// Since it returns an immediate response after processing the headers, the rest of the methods of the
// Processor are not implemented. Those should never be called.
type anthropicModelsProcessor struct {
	passThroughProcessor
	logger *slog.Logger
	models anthropic.ModelList
}

var _ Processor = (*anthropicModelsProcessor)(nil)

// NewAnthropicModelsProcessor creates a new processor that returns the list of declared models
// in the Anthropic API's model list format.
func NewAnthropicModelsProcessor(config *filterapi.RuntimeConfig, requestHeaders map[string]string, logger *slog.Logger, isUpstreamFilter bool, _ bool) (Processor, error) {
	if isUpstreamFilter {
		return passThroughProcessor{}, nil
	}

	host := requestHost(requestHeaders)
	selectedModels := selectModelsForHost(host, config)

	modelList := anthropic.ModelList{
		Data: make([]anthropic.Model, 0, len(selectedModels)),
	}
	for _, m := range selectedModels {
		modelList.Data = append(modelList.Data, anthropic.Model{
			ID:          m.Name,
			Type:        "model",
			DisplayName: m.Name,
			CreatedAt:   m.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	if len(modelList.Data) > 0 {
		modelList.FirstID = modelList.Data[0].ID
		modelList.LastID = modelList.Data[len(modelList.Data)-1].ID
	}
	return &anthropicModelsProcessor{logger: logger.With("host", host), models: modelList}, nil
}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (m *anthropicModelsProcessor) ProcessRequestHeaders(_ context.Context, _ *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
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
