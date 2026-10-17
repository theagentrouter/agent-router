// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"log/slog"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

// ProcessorFactory is the factory function used to create new instances of a processor.
type ProcessorFactory func(_ *filterapi.RuntimeConfig, _ map[string]string, _ *slog.Logger, isUpstreamFilter bool, enableRedaction bool) (Processor, error)

// Processor is the interface for the processor which corresponds to a single gRPC stream per the external processor filter.
// This decouples the processor implementation detail from the server implementation.
//
// This can be either a router filter level processor or an upstream filter level processor.
type Processor interface {
	// ProcessRequestHeaders processes the request headers message.
	ProcessRequestHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error)
	// ProcessRequestBody processes the request body message.
	ProcessRequestBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error)
	// ProcessResponseHeaders processes the response headers message.
	ProcessResponseHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error)
	// ProcessResponseBody processes the response body message.
	ProcessResponseBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error)
	// SetBackend instructs the processor to set the backend to use for the request. This is only called
	// when the processor is used in the upstream filter.
	//
	// routerProcessor is the processor that is the "parent" which was used to determine the route at the
	// router level. It holds the additional state that can be used to determine the backend to use.
	SetBackend(ctx context.Context, backend *filterapi.RuntimeBackend, routeName string, routerProcessor Processor) error
	// OnStreamTerminate is called once the gRPC stream this processor belongs to is torn down,
	// whether the request completed, the downstream client went away, or the context was canceled.
	// It gives the processor a chance to finalize state that the response path did not reach.
	OnStreamTerminate()
}

// passThroughProcessor implements the Processor interface.
type passThroughProcessor struct{}

// OnStreamTerminate implements [Processor.OnStreamTerminate].
func (p passThroughProcessor) OnStreamTerminate() {}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
func (p passThroughProcessor) ProcessRequestHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestHeaders{}}, nil
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (p passThroughProcessor) ProcessRequestBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}, nil
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (p passThroughProcessor) ProcessResponseHeaders(context.Context, *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{}}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (p passThroughProcessor) ProcessResponseBody(context.Context, *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseBody{}}, nil
}

// SetBackend implements [Processor.SetBackend].
func (p passThroughProcessor) SetBackend(context.Context, *filterapi.RuntimeBackend, string, Processor) error {
	return nil
}
