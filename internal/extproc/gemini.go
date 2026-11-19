// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
)

// NewGeminiGenerateContentFactory creates the processors for Gemini Developer API generation.
func NewGeminiGenerateContentFactory(prefix string, f metrics.Factory, tracer tracingapi.GeminiGenerateContentTracer) ProcessorFactory {
	upstream := NewFactory(f, tracer, endpointspec.GeminiGenerateContentEndpointSpec{})
	return func(config *filterapi.RuntimeConfig, headers map[string]string, logger *slog.Logger, isUpstream, enableRedaction bool) (Processor, error) {
		if isUpstream {
			return upstream(config, headers, logger, true, enableRedaction)
		}
		if headers[":method"] != http.MethodPost {
			return nil, errNoProcessor
		}
		requestPath, err := gcp.ParseGeminiGenerateContentPath(prefix, headers[":path"])
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errNoProcessor, err)
		}
		// The retry headers must not carry the client's query credentials.
		headers[":path"] = requestPath.Path(prefix, requestPath.Model)
		spec := endpointspec.GeminiGenerateContentEndpointSpec{Path: requestPath}
		return NewFactory(f, tracer, spec)(config, headers, logger, false, enableRedaction)
	}
}
