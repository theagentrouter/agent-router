// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

import (
	"context"
	"fmt"

	"github.com/google/cel-go/cel"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
)

// BackendAuthHandler is the interface that deals with the backend auth for a specific backend.
type BackendAuthHandler interface {
	// Do performs the backend auth, and make changes to the request headers passed in as `requestHeaders`.
	// It also returns a list of headers that were added or modified as a slice of key-value pairs.
	Do(ctx context.Context, requestHeaders map[string]string, mutatedBody []byte) ([]internalapi.Header, error)
}

// NewBackendAuthHandlerFunc is a function type that creates a new BackendAuthHandler for a given BackendAuth configuration.
type NewBackendAuthHandlerFunc func(ctx context.Context, auth *BackendAuth) (BackendAuthHandler, error)

// RuntimeConfig is the runtime filter configuration that is derived from the filterapi.Config.
type RuntimeConfig struct {
	// UUID is the unique identifier of the filter configuration, inherited from filterapi.Config.
	UUID string
	// GlobalRequestCosts is the list of gateway-level default request costs.
	// These have no RouteName and apply to all routes unless overridden.
	GlobalRequestCosts []RuntimeGlobalRequestCost
	// RequestCosts is the list of route-scoped request costs.
	// Each entry has a RouteName identifying the route it applies to.
	RequestCosts []RuntimeRequestCost
	// DeclaredModels is the list of declared models.
	DeclaredModels []Model
	// ModelsByHost maps hostnames to their specific model lists for per-host filtering. Each entry already includes
	// UnscopedModels so that routes without hostname scoping remain visible on host-matched requests.
	ModelsByHost map[string][]Model
	// UnscopedModels is the fallback returned for hosts that don't match any ModelsByHost entry. Populated from routes
	// that did NOT declare hostnames; kept separate from DeclaredModels so we don't leak scoped models to unknown hosts.
	UnscopedModels []Model
	// ModelsByHostAndScope maps hostname to header-scoped model groups for per-header filtering of /v1/models.
	ModelsByHostAndScope map[string]HeaderScopedModels
	// Backends is the map of backends by name.
	Backends map[string]*RuntimeBackend
}

// RuntimeBackend is a filter backend with its auth handler that is derived from the filterapi.Backend configuration.
type RuntimeBackend struct {
	// Backend is the filter backend configuration.
	Backend *Backend
	// Handler is the backend auth handler.
	Handler BackendAuthHandler
}

// RuntimeGlobalRequestCost is the configuration for gateway-level default request costs.
// This is derived from the filterapi.GlobalLLMRequestCost configuration, and includes the compiled CEL program if provided.
type RuntimeGlobalRequestCost struct {
	*GlobalLLMRequestCost
	CELProg cel.Program
}

// RuntimeRequestCost is the configuration for route-scoped request costs, optionally with a CEL program.
// This is derived from the filterapi.LLMRequestCost configuration, and includes the compiled CEL program if provided.
type RuntimeRequestCost struct {
	*LLMRequestCost
	CELProg cel.Program
}

// NewRuntimeConfig creates a new runtime filter configuration from the given filterapi.Config and a function to create backend auth handlers.
func NewRuntimeConfig(ctx context.Context, config *Config, fn NewBackendAuthHandlerFunc) (*RuntimeConfig, error) {
	backends := make(map[string]*RuntimeBackend, len(config.Backends))
	for i := range config.Backends {
		b := &config.Backends[i]
		var h BackendAuthHandler
		if b.Auth != nil {
			var err error
			h, err = fn(ctx, b.Auth)
			if err != nil {
				return nil, fmt.Errorf("cannot create backend auth handler: %w", err)
			}
		}

		backends[b.Name] = &RuntimeBackend{Backend: b, Handler: h}
	}

	// Compile CEL programs for GlobalLLMRequestCosts (gateway-level defaults).
	globalCosts := make([]RuntimeGlobalRequestCost, 0, len(config.GlobalLLMRequestCosts))
	for i := range config.GlobalLLMRequestCosts {
		c := &config.GlobalLLMRequestCosts[i]
		var prog cel.Program
		if c.CEL != "" {
			var err error
			prog, err = llmcostcel.NewProgram(c.CEL)
			if err != nil {
				return nil, fmt.Errorf("cannot create CEL program for global cost: %w", err)
			}
		}
		globalCosts = append(globalCosts, RuntimeGlobalRequestCost{
			GlobalLLMRequestCost: c,
			CELProg:              prog,
		})
	}

	// Compile CEL programs for LLMRequestCosts (route-scoped).
	// Validate that all route-scoped costs have a non-empty RouteName.
	costs := make([]RuntimeRequestCost, 0, len(config.LLMRequestCosts))
	for i := range config.LLMRequestCosts {
		c := &config.LLMRequestCosts[i]
		if c.RouteName == "" {
			return nil, fmt.Errorf("route-scoped LLMRequestCost with metadataKey=%q must have non-empty RouteName", c.MetadataKey)
		}
		var prog cel.Program
		if c.CEL != "" {
			var err error
			prog, err = llmcostcel.NewProgram(c.CEL)
			if err != nil {
				return nil, fmt.Errorf("cannot create CEL program for cost: %w", err)
			}
		}
		costs = append(costs, RuntimeRequestCost{LLMRequestCost: c, CELProg: prog})
	}

	return &RuntimeConfig{
		UUID:                 config.UUID,
		Backends:             backends,
		GlobalRequestCosts:   globalCosts,
		RequestCosts:         costs,
		DeclaredModels:       config.Models,
		ModelsByHost:         config.ModelsByHost,
		UnscopedModels:       config.UnscopedModels,
		ModelsByHostAndScope: config.ModelsByHostAndScope,
	}, nil
}
