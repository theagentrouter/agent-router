// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3http "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/google/cel-go/cel"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/bodymutator"
	"github.com/envoyproxy/ai-gateway/internal/endpointspec"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/headermutator"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/llmcostcel"
	"github.com/envoyproxy/ai-gateway/internal/metrics"
	"github.com/envoyproxy/ai-gateway/internal/tracing/tracingapi"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

// LogRequestHeaderAttributes is the mapping of request headers to log as dynamic metadata attributes.
// This is configured at the startup of the extproc server.
var LogRequestHeaderAttributes map[string]string

// NewFactory creates a ProcessorFactory with the given parameters.
//
// Type Parameters:
// * ReqT: The request type.
// * RespT: The response type.
// * RespChunkT: The chunk type for streaming responses.
//
// Parameters:
// * f: Metrics factory for creating metrics instances.
// * tracer: Request tracer for tracing requests and responses.
// * parseBody: Function to parse the request body.
// * selectTranslator: Function to select the appropriate translator based on the output schema.
//
// Returns:
// * ProcessorFactory: A factory function to create processors based on the configuration.
func NewFactory[ReqT any, RespT any, RespChunkT any, EndpointSpecT endpointspec.Spec[ReqT, RespT, RespChunkT]](
	f metrics.Factory,
	tracer tracingapi.RequestTracer[ReqT, RespT, RespChunkT],
	_ EndpointSpecT, // This is a type marker to bind EndpointSpecT without specifying ReqT, RespT, RespChunkT explicitly.
) ProcessorFactory {
	return func(config *filterapi.RuntimeConfig, requestHeaders map[string]string, logger *slog.Logger, isUpstreamFilter bool, enableRedaction bool) (Processor, error) {
		logger = logger.With("isUpstreamFilter", fmt.Sprintf("%v", isUpstreamFilter))
		if !isUpstreamFilter {
			return newRouterProcessor[ReqT, RespT, RespChunkT, EndpointSpecT](config, requestHeaders, logger, tracer, enableRedaction), nil
		}
		return newUpstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT](requestHeaders, f.NewMetrics(), logger), nil
	}
}

type (
	// routerProcessor implements [Processor] for the router filter for the standard LLM endpoints.
	routerProcessor[ReqT, RespT, RespChunkT any, EndpointSpecT endpointspec.Spec[ReqT, RespT, RespChunkT]] struct {
		eh EndpointSpecT

		passThroughProcessor
		// upstreamFilter is the upstream filter that is used to process the request at the upstream filter.
		// This will be updated when the request is retried.
		//
		// On the response handling path, we don't need to do any operation until successful, so we use the implementation
		// of the upstream filter to handle the response at the router filter.
		//
		// TODO: this is a bit of a hack and dirty workaround, so revert this to a cleaner design later.
		upstreamFilter *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]
		logger         *slog.Logger
		config         *filterapi.RuntimeConfig
		requestHeaders map[string]string
		// originalRequestBody is the original request body that is passed to the upstream filter.
		// This is used to perform the transformation of the request body on the original input
		// when the request is retried.
		originalRequestBody    *ReqT
		originalRequestBodyRaw []byte
		originalModel          internalapi.OriginalModel
		forceBodyMutation      bool
		// tracer is the tracer used for requests.
		tracer tracingapi.RequestTracer[ReqT, RespT, RespChunkT]
		// span is the tracing span for this request, created in ProcessRequestBody.
		span tracingapi.Span[RespT, RespChunkT]
		// upstreamFilterCount is the number of upstream filters that have been processed.
		// This is used to determine if the request is a retry request.
		upstreamFilterCount int
		// localReplyEmitted records that the gateway answered the request itself with an
		// ImmediateResponse. Envoy sends that local reply back through the response path,
		// where it would otherwise be handled as if it came from the upstream.
		localReplyEmitted bool
		stream            bool
		debugLogEnabled   bool
		enableRedaction   bool
	}
	// upstreamProcessor implements [Processor] for the upstream filter for the standard LLM endpoints.
	//
	// This will be used together with [routerProcessor].
	upstreamProcessor[ReqT, RespT, RespChunkT any, EndpointSpecT endpointspec.Spec[ReqT, RespT, RespChunkT]] struct {
		parent *routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]

		logger             *slog.Logger
		requestHeaders     map[string]string
		responseHeaders    map[string]string
		responseEncoding   string
		compressedBuf      []byte // accumulates raw compressed bytes across streaming chunks
		decompressedOffset int    // tracks decompressed bytes already returned
		translator         translator.Translator[ReqT, tracingapi.Span[RespT, RespChunkT]]
		modelNameOverride  internalapi.ModelNameOverride
		headerMutator      *headermutator.HeaderMutator
		bodyMutator        *bodymutator.BodyMutator
		backendName        string
		routeName          string
		handler            filterapi.BackendAuthHandler
		// cost is the cost of the request that is accumulated during the processing of the response.
		costs metrics.TokenUsage
		// metrics tracking.
		metrics metrics.Metrics
	}
)

func newRouterProcessor[ReqT, RespT, RespChunkT any, EndpointSpecT endpointspec.Spec[ReqT, RespT, RespChunkT]](
	config *filterapi.RuntimeConfig,
	requestHeaders map[string]string,
	logger *slog.Logger,
	tracer tracingapi.RequestTracer[ReqT, RespT, RespChunkT],
	enableRedaction bool,
) *routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT] {
	debugLogEnabled := logger.Enabled(context.Background(), slog.LevelDebug)
	return &routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]{
		config:            config,
		requestHeaders:    requestHeaders,
		logger:            logger,
		tracer:            tracer,
		forceBodyMutation: false,
		debugLogEnabled:   debugLogEnabled,
		enableRedaction:   enableRedaction,
	}
}

func newUpstreamProcessor[ReqT, RespT, RespChunkT any, EndpointSpecT endpointspec.Spec[ReqT, RespT, RespChunkT]](
	reqHeader map[string]string, metrics metrics.Metrics,
	logger *slog.Logger,
) *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT] {
	return &upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]{
		requestHeaders: reqHeader,
		metrics:        metrics,
		logger:         logger,
	}
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (r *routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessResponseHeaders(ctx context.Context, headerMap *corev3.HeaderMap) (*extprocv3.ProcessingResponse, error) {
	// If the request failed to route and/or immediate response was returned before the upstream filter was set,
	// r.upstreamFilter can be nil.
	if r.upstreamFilter != nil { // See the comment on the "upstreamFilter" field.
		return r.upstreamFilter.ProcessResponseHeaders(ctx, headerMap)
	}
	return r.passThroughProcessor.ProcessResponseHeaders(ctx, headerMap)
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (r *routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessResponseBody(ctx context.Context, body *extprocv3.HttpBody) (resp *extprocv3.ProcessingResponse, err error) {
	// If the request failed to route and/or immediate response was returned before the upstream filter was set,
	// r.upstreamFilter can be nil.
	if r.upstreamFilter != nil { // See the comment on the "upstreamFilter" field.
		resp, err = r.upstreamFilter.ProcessResponseBody(ctx, body)
	} else {
		resp, err = r.passThroughProcessor.ProcessResponseBody(ctx, body)
	}
	return
}

// formatUserFacingErrorJSON formats a user-facing error as a JSON response body.
// Returns JSON in format: {"type":"error","error":{"type":"<errorType>","code":"<statusCode>","message":"<message>"}}
func formatUserFacingErrorJSON(errorType string, statusCode int, message string) []byte {
	return fmt.Appendf(nil, `{"type":"error","error":{"type":"%s","code":"%d","message":"%s"}}`,
		errorType, statusCode, message)
}

// respondLocally answers the request from the gateway itself instead of dispatching it upstream.
//
// The immediate response is delivered as an Envoy local reply, which travels back through the
// response path. It records that on the parent so [upstreamProcessor.ProcessResponseBody] can
// skip it, and ends the span here because the response path no longer will.
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) respondLocally(ctx context.Context, statusCode int, errorType, message string) *extprocv3.ProcessingResponse {
	u.metrics.RecordRequestCompletion(ctx, false, u.requestHeaders)
	resp := createUserFacingErrorResponse(statusCode, errorType, message)
	u.parent.localReplyEmitted = true
	if u.parent.span != nil {
		u.parent.span.EndSpanOnError(statusCode, resp.GetImmediateResponse().GetBody())
	}
	return resp
}

// createUserFacingErrorResponse creates an ImmediateResponse for user-facing errors with JSON body.
func createUserFacingErrorResponse(statusCode int, errorType string, message string) *extprocv3.ProcessingResponse {
	body := formatUserFacingErrorJSON(errorType, statusCode, message)
	headerMutation := &extprocv3.HeaderMutation{}
	setHeader(headerMutation, "content-type", "application/json")
	setHeader(headerMutation, "content-length", strconv.Itoa(len(body)))

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status:     &typev3.HttpStatus{Code: typev3.StatusCode(statusCode)}, // #nosec G115 - HTTP status codes are always in valid int32 range
				Headers:    headerMutation,
				Body:       body,
				GrpcStatus: &extprocv3.GrpcStatus{Status: uint32(codes.InvalidArgument)},
			},
		},
	}
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (r *routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessRequestBody(ctx context.Context, rawBody *extprocv3.HttpBody) (*extprocv3.ProcessingResponse, error) {
	var (
		originalModel       internalapi.OriginalModel
		body                *ReqT
		stream              bool
		mutatedOriginalBody []byte
		err                 error
	)
	costConfigured := len(r.config.RequestCosts) > 0 || len(r.config.GlobalRequestCosts) > 0
	contentType := r.requestHeaders["content-type"]
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		originalModel, body, stream, mutatedOriginalBody, err = r.eh.ParseMultipartBody(rawBody.Body, contentType, costConfigured)
	} else {
		originalModel, body, stream, mutatedOriginalBody, err = r.eh.ParseBody(rawBody.Body, costConfigured)
	}
	if err != nil {
		if userFacingErr := internalapi.GetUserFacingError(err); userFacingErr != nil {
			// return to user as 400 -  e.g., "malformed request: failed to parse JSON for /v1/chat/completions"
			r.logger.Error("returning user-facing error for malformed request", slog.String("error", err.Error()))
			return createUserFacingErrorResponse(400, "BadRequest", userFacingErr.Error()), nil
		}
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	// Use the request-scoped logger from context if available, otherwise fall back to processor logger
	logger := loggerFromContext(ctx)
	if logger == nil {
		logger = r.logger
	}

	// Only log parsed request body when redaction is enabled
	if r.debugLogEnabled && r.enableRedaction {
		if redactedBody, err := r.eh.RedactSensitiveInfoFromRequest(body); err != nil {
			logger.Warn("failed to redact sensitive info from request, ignoring and continuing", slog.Any("error", err))
		} else {
			if jsonBody, err := json.Marshal(redactedBody); err != nil {
				logger.Error("failed to marshal redacted request for logging, ignoring and continuing", slog.Any("error", err))
			} else {
				logger.Debug("request body processing", slog.Any("request", string(jsonBody)))
			}
		}
	}

	if mutatedOriginalBody != nil {
		r.originalRequestBodyRaw = mutatedOriginalBody
		r.forceBodyMutation = true
	} else {
		r.originalRequestBodyRaw = rawBody.Body
	}

	r.requestHeaders[internalapi.ModelNameHeaderKeyDefault] = originalModel

	var additionalHeaders []*corev3.HeaderValueOption
	additionalHeaders = append(additionalHeaders, &corev3.HeaderValueOption{
		// Set the original model to the request header with the key `x-ai-eg-model`.
		// This header is owned by the gateway, so overwrite any client-supplied value instead of appending.
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		Header:       &corev3.HeaderValue{Key: internalapi.ModelNameHeaderKeyDefault, RawValue: []byte(originalModel)},
	})
	originalPath := r.requestHeaders[":path"]
	// These original-path headers are owned by extproc, so set them unconditionally.
	// A client-supplied or pre-existing value must not shadow the gateway's own value,
	// as downstream logic (e.g. processor lookup on retry) keys off it.
	r.requestHeaders[originalPathHeader] = originalPath
	additionalHeaders = append(additionalHeaders, &corev3.HeaderValueOption{
		// Overwrite unconditionally so a client-supplied or pre-existing value is replaced, not appended.
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		Header:       &corev3.HeaderValue{Key: originalPathHeader, RawValue: []byte(originalPath)},
	})
	r.requestHeaders[internalapi.EnvoyOriginalPathHeader] = originalPath
	additionalHeaders = append(additionalHeaders, &corev3.HeaderValueOption{
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		Header:       &corev3.HeaderValue{Key: internalapi.EnvoyOriginalPathHeader, RawValue: []byte(originalPath)},
	})
	r.originalModel = originalModel
	r.originalRequestBody = body
	r.stream = stream

	// Tracing may need to inject headers, so create a header mutation here.
	headerMutation := &extprocv3.HeaderMutation{
		SetHeaders: additionalHeaders,
	}
	r.span = r.tracer.StartSpanAndInjectHeaders(
		ctx,
		r.requestHeaders,
		&headerMutationCarrier{m: headerMutation},
		body,
		rawBody.Body,
	)

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation:  headerMutation,
					ClearRouteCache: true,
				},
			},
		},
	}, nil
}

func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) onRetry() bool {
	return u.parent.upstreamFilterCount > 1
}

// ProcessRequestHeaders implements [Processor.ProcessRequestHeaders].
//
// At the upstream filter, we already have the original request body at request headers phase.
// So, we simply do the translation and upstream auth at this stage, and send them back to Envoy
// with the status CONTINUE_AND_REPLACE. This allows Envoy to not send the request body again
// to the extproc.
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessRequestHeaders(ctx context.Context, _ *corev3.HeaderMap) (res *extprocv3.ProcessingResponse, err error) {
	defer func() {
		if err != nil {
			u.metrics.RecordRequestCompletion(ctx, false, u.requestHeaders)
		}
	}()

	// Start tracking metrics for this request.
	u.metrics.StartRequest(u.requestHeaders)
	// Set the original model from the request body before any overrides
	u.metrics.SetOriginalModel(u.parent.originalModel)
	// Set the request model for metrics from the original model or override if applied.
	reqModel := cmp.Or(u.requestHeaders[internalapi.ModelNameHeaderKeyDefault], u.parent.originalModel)
	u.metrics.SetRequestModel(reqModel)

	// We force the body mutation in the following cases:
	// * The request is a retry request because the body mutation might have happened the previous iteration.
	// * The request is a streaming request, and the IncludeUsage option is set to false since we need to ensure that
	//	the token usage is calculated correctly without being bypassed.
	forceBodyMutation := u.onRetry() || u.parent.forceBodyMutation
	newHeaders, newBody, err := u.translator.RequestBody(u.parent.originalRequestBodyRaw, u.parent.originalRequestBody, forceBodyMutation)
	if err != nil {
		if userFacingErr := internalapi.GetUserFacingError(err); userFacingErr != nil {
			// return to user as 422 -  e.g., "invalid request body: tool_choice type not supported"
			u.logger.Info("returning user-facing error for invalid request", slog.String("error", err.Error()))
			return u.respondLocally(ctx, 422, "UnprocessableEntity", userFacingErr.Error()), nil
		}
		return nil, fmt.Errorf("failed to transform request: %w", err)
	}

	headerMutation, bodyMutation := mutationsFromTranslationResult(newHeaders, newBody)

	// Apply header mutations from the route and also restore original headers on retry.
	if h := u.headerMutator; h != nil {
		sets, removes := u.headerMutator.Mutate(u.requestHeaders, u.onRetry())
		headerMutation.RemoveHeaders = append(headerMutation.RemoveHeaders, removes...)
		for _, hdr := range sets {
			headerMutation.SetHeaders = append(headerMutation.SetHeaders, &corev3.HeaderValueOption{
				AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
				Header: &corev3.HeaderValue{
					Key:      hdr.Key(),
					RawValue: []byte(hdr.Value()),
				},
			})
		}
	}

	// Decide whether the upstream filter should replace the request body at
	// all. If the translator emitted no body, no backend HTTPBodyMutation is
	// configured, and we're not forcing body replay (retry or
	// streaming-without-usage), then issuing CONTINUE_AND_REPLACE with the
	// captured original body would clobber any body mutation applied by an
	// earlier ext_proc filter in the chain.
	mutatorHasMutations := u.bodyMutator != nil && u.bodyMutator.HasMutations()
	wantBodyReplace := bodyMutation != nil || forceBodyMutation || mutatorHasMutations

	if wantBodyReplace {
		// Apply body mutations from the route and also restore original body on retry.
		bodyMutation = applyBodyMutation(u.bodyMutator, bodyMutation, u.parent.originalRequestBodyRaw, u.logger)
	}

	// Ensure bodyMutation is not nil for subsequent processing
	if bodyMutation == nil {
		bodyMutation = &extprocv3.BodyMutation{}
	}

	for _, h := range headerMutation.SetHeaders {
		u.requestHeaders[h.Header.Key] = string(h.Header.RawValue)
	}

	// x-ai-eg-upstream-host is an internal, controller-derived value consumed by backend auth handlers
	// (e.g. the AWS handler, via the upstream-host metadata attribute). Strip any copy — including one a
	// downstream client spoofed — so it never egresses to the upstream provider.
	headerMutation.RemoveHeaders = append(headerMutation.RemoveHeaders, internalapi.UpstreamHostHeader)

	if h := u.handler; h != nil {
		var hdrs []internalapi.Header
		hdrs, err = h.Do(ctx, u.requestHeaders, bodyMutation.GetBody())
		if err != nil {
			if errors.Is(err, backendauth.ErrCredentialMissing) {
				return u.respondLocally(ctx, 401, "Unauthorized", "missing upstream credential"), nil
			}
			return nil, fmt.Errorf("failed to do auth request: %w", err)
		}
		for _, h := range hdrs {
			headerMutation.SetHeaders = append(headerMutation.SetHeaders, &corev3.HeaderValueOption{
				AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
				Header:       &corev3.HeaderValue{Key: h.Key(), RawValue: []byte(h.Value())},
			})
		}
	}

	if !wantBodyReplace {
		// No body change -> no content-length restamp; emit CONTINUE so Envoy
		// keeps whatever body the previous filter in the chain produced.
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_RequestHeaders{
				RequestHeaders: &extprocv3.HeadersResponse{
					Response: &extprocv3.CommonResponse{
						HeaderMutation: headerMutation,
						Status:         extprocv3.CommonResponse_CONTINUE,
					},
				},
			},
			DynamicMetadata: mergeDynamicMetadata(
				buildBackendDynamicMetadata(u.backendName),
				buildRequestHeaderDynamicMetadata(u.requestHeaders),
			),
		}, nil
	}

	var dm *structpb.Struct
	if bm := bodyMutation.GetBody(); bm != nil {
		dm = buildContentLengthDynamicMetadataOnRequest(len(bm))
	}
	dm = mergeDynamicMetadata(dm, buildBackendDynamicMetadata(u.backendName))
	dm = mergeDynamicMetadata(dm, buildRequestHeaderDynamicMetadata(u.requestHeaders))
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: headerMutation, BodyMutation: bodyMutation,
					Status: extprocv3.CommonResponse_CONTINUE_AND_REPLACE,
				},
			},
		},
		DynamicMetadata: dm,
	}, nil
}

// ProcessRequestBody implements [Processor.ProcessRequestBody].
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessRequestBody(context.Context, *extprocv3.HttpBody) (res *extprocv3.ProcessingResponse, err error) {
	panic("BUG: ProcessRequestBody should not be called in the upstream filter")
}

// ProcessResponseHeaders implements [Processor.ProcessResponseHeaders].
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessResponseHeaders(ctx context.Context, headers *corev3.HeaderMap) (res *extprocv3.ProcessingResponse, err error) {
	// See the same check in ProcessResponseBody: the headers of a local reply are ours as well,
	// so they are passed through untouched.
	if u.parent.localReplyEmitted {
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseHeaders{
				ResponseHeaders: &extprocv3.HeadersResponse{},
			},
		}, nil
	}

	defer func() {
		if err != nil {
			u.metrics.RecordRequestCompletion(ctx, false, u.requestHeaders)
		}
	}()

	u.responseHeaders = headersToMap(headers)
	if enc := u.responseHeaders["content-encoding"]; enc != "" {
		u.responseEncoding = enc
	}
	// Reset streaming decompression state for new response (important for retries).
	u.compressedBuf = nil
	u.decompressedOffset = 0
	newHeaders, err := u.translator.ResponseHeaders(u.responseHeaders)
	if err != nil {
		return nil, fmt.Errorf("failed to transform response headers: %w", err)
	}
	var mode *extprocv3http.ProcessingMode
	if u.parent.stream && u.responseHeaders[":status"] == "200" {
		// We only stream the response if the status code is 200 and the response is a stream.
		mode = &extprocv3http.ProcessingMode{ResponseBodyMode: extprocv3http.ProcessingMode_STREAMED}
	}
	headerMutation, _ := mutationsFromTranslationResult(newHeaders, nil)
	// When switching to streamed mode, remove content-length so Envoy does
	// not truncate the response to the first body chunk. The upstream may
	// include content-length (e.g. computed by a prior hop's HTTP/2 codec
	// when transfer-encoding: chunked was copied by an EPP ext_proc), which
	// is invalid for a streaming SSE response.
	if mode != nil {
		headerMutation.RemoveHeaders = append(headerMutation.RemoveHeaders, "content-length")
	}
	return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseHeaders{
		ResponseHeaders: &extprocv3.HeadersResponse{
			Response: &extprocv3.CommonResponse{HeaderMutation: headerMutation},
		},
	}, ModeOverride: mode}, nil
}

// ProcessResponseBody implements [Processor.ProcessResponseBody].
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) ProcessResponseBody(ctx context.Context, body *extprocv3.HttpBody) (res *extprocv3.ProcessingResponse, err error) {
	// A local reply the gateway produced itself is not an upstream response: translating it would
	// rewrite the body we just wrote, and completion was already recorded when it was produced.
	// This returns before the deferred recording below is registered.
	if u.parent.localReplyEmitted {
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseBody{
				ResponseBody: &extprocv3.BodyResponse{},
			},
		}, nil
	}

	recordRequestCompletionErr := false
	defer func() {
		if err != nil || recordRequestCompletionErr {
			u.metrics.RecordRequestCompletion(ctx, false, u.requestHeaders)
			return
		}
		if body.EndOfStream {
			u.metrics.RecordRequestCompletion(ctx, true, u.requestHeaders)
		}
	}()

	// Decompress the body if needed.
	// For streaming responses with content-encoding, use stateful decompression
	// that accumulates compressed bytes across chunks.
	var decodingResult contentDecodingResult
	if u.parent.stream && u.responseEncoding != "" {
		decodingResult, err = u.decodeStreamingContent(body.Body, body.EndOfStream)
	} else {
		decodingResult, err = decodeContentIfNeeded(body.Body, u.responseEncoding)
	}
	if err != nil {
		return nil, err
	}

	// Assume all responses have a valid status code header.
	if code, _ := strconv.Atoi(u.responseHeaders[":status"]); !isGoodStatusCode(code) {
		var newHeaders []internalapi.Header
		var newBody []byte
		newHeaders, newBody, err = u.translator.ResponseError(u.responseHeaders, decodingResult.reader)
		if err != nil {
			return nil, fmt.Errorf("failed to transform response error: %w", err)
		}
		headerMutation, bodyMutation := mutationsFromTranslationResult(newHeaders, newBody)
		// Remove content-encoding header if original body encoded but was mutated in the processor.
		headerMutation = removeContentEncodingIfNeeded(headerMutation, bodyMutation, decodingResult.isEncoded)
		if u.parent.span != nil {
			b := bodyMutation.GetBody()
			if b == nil {
				b = body.Body
			}
			u.parent.span.EndSpanOnError(code, b)
		}
		// Mark so the deferred handler records failure.
		recordRequestCompletionErr = true
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseBody{
				ResponseBody: &extprocv3.BodyResponse{
					Response: &extprocv3.CommonResponse{
						HeaderMutation: headerMutation,
						BodyMutation:   bodyMutation,
					},
				},
			},
		}, nil
	}

	newHeaders, newBody, tokenUsage, responseModel, err := u.translator.ResponseBody(u.responseHeaders, decodingResult.reader, body.EndOfStream, u.parent.span)
	if err != nil {
		return nil, fmt.Errorf("failed to transform response: %w", err)
	}
	headerMutation, bodyMutation := mutationsFromTranslationResult(newHeaders, newBody)

	// Remove content-encoding header if original body encoded but was mutated in the processor.
	headerMutation = removeContentEncodingIfNeeded(headerMutation, bodyMutation, decodingResult.isEncoded)

	resp := &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: headerMutation,
					BodyMutation:   bodyMutation,
				},
			},
		},
	}

	// Translator reports the latest cumulative token usage which we use to override existing costs.
	u.costs.Override(tokenUsage)

	// Set the response model for metrics
	u.metrics.SetResponseModel(responseModel)

	// Record metrics.
	if u.parent.stream {
		// Token latency is only recorded for streaming responses, otherwise it doesn't make sense since
		// these metrics are defined as a difference between the two output events.
		out, _ := u.costs.OutputTokens()
		u.metrics.RecordTokenLatency(ctx, out, body.EndOfStream, u.requestHeaders)
		// Emit usage once at end-of-stream using final totals.
		if body.EndOfStream {
			u.metrics.RecordTokenUsage(ctx, u.costs, u.requestHeaders)
		}
	} else {
		u.metrics.RecordTokenUsage(ctx, u.costs, u.requestHeaders)
	}

	// Build dynamic metadata as soon as the accumulated usage changes (i.e. the chunk that carries
	// the usage payload), not only at end-of-stream. This ensures the access log still captures usage
	// even if the downstream client disconnects right after the terminal chunk, before EndOfStream
	// is observed by the extproc. The EndOfStream write below remains as the final refresh.
	if (body.EndOfStream || !tokenUsage.IsZero()) && (len(u.parent.config.GlobalRequestCosts) > 0 || len(u.parent.config.RequestCosts) > 0) {
		metadata, err := buildDynamicMetadata(u.parent.config.GlobalRequestCosts, u.parent.config.RequestCosts, &u.costs, u.requestHeaders, u.backendName, u.routeName, responseModel)
		if err != nil {
			return nil, fmt.Errorf("failed to build dynamic metadata: %w", err)
		}
		if u.parent.stream {
			// Adding token latency information to metadata.
			u.mergeWithTokenLatencyMetadata(metadata)
		}
		resp.DynamicMetadata = metadata
	}

	if body.EndOfStream && u.parent.span != nil {
		u.parent.span.EndSpan()
	}
	return resp, nil
}

// decodeStreamingContent handles decompression for streaming responses with content-encoding.
// It accumulates raw compressed bytes across chunks and re-decompresses from the beginning each time,
// returning only the newly decompressed data. This is necessary because gzip streams are stateful
// and a new decompressor cannot be created mid-stream without the full preceding data.
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) decodeStreamingContent(chunk []byte, endOfStream bool) (contentDecodingResult, error) {
	u.compressedBuf = append(u.compressedBuf, chunk...)
	decodingResult, err := decodeContentIfNeeded(u.compressedBuf, u.responseEncoding)
	if err != nil {
		return contentDecodingResult{}, err
	}
	allDecompressed, readErr := io.ReadAll(decodingResult.reader)
	if readErr != nil {
		if endOfStream || !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return contentDecodingResult{}, fmt.Errorf("failed to decompress streaming content: %w", readErr)
		}
		// For non-final chunks, ErrUnexpectedEOF is expected: the gzip stream is incomplete
		// (footer not yet received), but all data up to this point is valid.
	}
	newData := allDecompressed[u.decompressedOffset:]
	u.decompressedOffset = len(allDecompressed)
	return contentDecodingResult{reader: bytes.NewReader(newData), isEncoded: true}, nil
}

// SetBackend implements [Processor.SetBackend].
func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) SetBackend(ctx context.Context, backend *filterapi.RuntimeBackend, routeName string, routeProcessor Processor) (err error) {
	defer func() {
		if err != nil {
			u.metrics.RecordRequestCompletion(ctx, false, u.requestHeaders)
		}
	}()
	rp, ok := routeProcessor.(*routerProcessor[ReqT, RespT, RespChunkT, EndpointSpecT])
	if !ok {
		panic(fmt.Sprintf("BUG: expected routeProcessor to be of type *routerProcessor[%T], got %T", rp, routeProcessor))
	}
	rp.upstreamFilterCount++
	u.metrics.SetBackend(backend.Backend)
	// Some semantic conventions record the provider, which is only known now
	// that routing has resolved a backend.
	if bs, ok := rp.span.(tracingapi.BackendSpan); ok {
		bs.RecordBackend(tracingapi.Backend{
			Schema: string(backend.Backend.Schema.Name),
			Name:   backend.Backend.Name,
		})
	}
	u.modelNameOverride = backend.Backend.ModelNameOverride
	u.backendName = backend.Backend.Name
	u.routeName = routeName
	u.handler = backend.Handler
	u.headerMutator = headermutator.NewHeaderMutator(backend.Backend.HeaderMutation, rp.requestHeaders)
	u.bodyMutator = bodymutator.NewBodyMutator(backend.Backend.BodyMutation, rp.originalRequestBodyRaw)
	// Header-derived labels/CEL must be able to see the overridden request model.
	if u.modelNameOverride != "" {
		u.requestHeaders[internalapi.ModelNameHeaderKeyDefault] = u.modelNameOverride
	}
	u.parent = rp // Set parent before GetTranslator so it can access rp.eh

	u.translator, err = u.parent.eh.GetTranslator(backend.Backend.Schema, u.modelNameOverride)
	if err != nil {
		return fmt.Errorf("failed to create translator for backend %s: %w", backend.Backend.Name, err)
	}
	if setter, ok := u.translator.(translator.ContentTypeSetter); ok {
		setter.SetContentType(rp.requestHeaders["content-type"])
	}
	rp.upstreamFilter = u // Only assign after translator is confirmed valid

	if headerSetter, ok := u.translator.(translator.RequestHeadersSetter); ok {
		headerSetter.SetRequestHeaders(u.requestHeaders)
	}

	if filters := backend.Backend.HeaderValueFilters; len(filters) > 0 {
		if filterSetter, ok := u.translator.(translator.HeaderValueFilterSetter); ok {
			for _, f := range filters {
				filterSetter.SetHeaderValueFilter(f.Name, f.Mode, f.Values)
			}
		}
	}

	switch redactor := u.translator.(type) {
	case translator.ResponseRedactor:
		redactor.SetRedactionConfig(u.parent.debugLogEnabled, u.parent.enableRedaction, u.logger)
	case translator.AnthropicResponseRedactor:
		redactor.SetRedactionConfig(u.parent.debugLogEnabled, u.parent.enableRedaction, u.logger)
	default:
		if u.parent.debugLogEnabled && u.parent.enableRedaction {
			u.logger.Debug("translator does not support redaction", slog.String("backend", backend.Backend.Name))
		}
	}

	return
}

func (u *upstreamProcessor[ReqT, RespT, RespChunkT, EndpointSpecT]) mergeWithTokenLatencyMetadata(metadata *structpb.Struct) {
	timeToFirstTokenMs := u.metrics.GetTimeToFirstTokenMs()
	interTokenLatencyMs := u.metrics.GetInterTokenLatencyMs()
	innerVal := metadata.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
	if innerVal == nil {
		innerVal = &structpb.Struct{Fields: map[string]*structpb.Value{}}
		metadata.Fields[internalapi.AIGatewayFilterMetadataNamespace] = structpb.NewStructValue(innerVal)
	}
	innerVal.Fields["token_latency_ttft"] = &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: timeToFirstTokenMs}}
	innerVal.Fields["token_latency_itl"] = &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: interTokenLatencyMs}}
}

// buildContentLengthDynamicMetadataOnRequest builds dynamic metadata for the request with content length.
//
// This is necessary to ensure that the content length can be set after the extproc filter has processed the request,
// which will happen in the header mutation filter.
//
// This is needed since the content length header is unconditionally cleared by Envoy as we use REPLACE_AND_CONTINUE
// processing mode in the request headers phase at upstream filter. This is sort of a workaround, and it is necessary
// for now.
func buildContentLengthDynamicMetadataOnRequest(contentLength int) *structpb.Struct {
	metadata := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			internalapi.AIGatewayFilterMetadataNamespace: {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"content_length": {
								Kind: &structpb.Value_NumberValue{NumberValue: float64(contentLength)},
							},
						},
					},
				},
			},
		},
	}
	return metadata
}

// buildBackendDynamicMetadata emits the backend resolved for this request by
// [upstreamProcessor.SetBackend]. Emitting it during decode is what makes it readable on
// the response path: Envoy applies route-level response header mutations in the router
// filter, which runs before this filter when encoding.
func buildBackendDynamicMetadata(backendName string) *structpb.Struct {
	if backendName == "" {
		return nil
	}
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			internalapi.AIGatewayFilterMetadataNamespace: {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{
						Fields: map[string]*structpb.Value{
							"backend_name": structpb.NewStringValue(backendName),
						},
					},
				},
			},
		},
	}
}

func buildRequestHeaderDynamicMetadata(requestHeaders map[string]string) *structpb.Struct {
	if len(LogRequestHeaderAttributes) == 0 {
		return nil
	}
	fields := make(map[string]*structpb.Value, len(LogRequestHeaderAttributes))
	for header, attr := range LogRequestHeaderAttributes {
		value, ok := requestHeaders[header]
		if !ok || value == "" {
			continue
		}
		fields[attr] = &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: value}}
	}
	if len(fields) == 0 {
		return nil
	}
	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			internalapi.AIGatewayFilterMetadataNamespace: {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{Fields: fields},
				},
			},
		},
	}
}

func mergeDynamicMetadata(base, extra *structpb.Struct) *structpb.Struct {
	if base == nil {
		return extra
	}
	if extra == nil {
		return base
	}
	baseFields := base.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
	if baseFields == nil {
		baseFields = &structpb.Struct{Fields: map[string]*structpb.Value{}}
		base.Fields[internalapi.AIGatewayFilterMetadataNamespace] = structpb.NewStructValue(baseFields)
	}
	extraFields := extra.Fields[internalapi.AIGatewayFilterMetadataNamespace].GetStructValue()
	if extraFields == nil {
		return base
	}
	for k, v := range extraFields.Fields {
		baseFields.Fields[k] = v
	}
	return base
}

// evalCost is a helper function that computes the cost value based on the cost type and CEL program.
func evalCost(costType filterapi.LLMRequestCostType, celProg cel.Program, costs *metrics.TokenUsage, requestHeaders map[string]string, backendName, routeName string) (uint64, error) {
	var cost uint64
	switch costType {
	case filterapi.LLMRequestCostTypeInputToken:
		v, _ := costs.InputTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeCachedInputToken:
		v, _ := costs.CachedInputTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeCacheCreationInputToken:
		v, _ := costs.CacheCreationInputTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeOutputToken:
		v, _ := costs.OutputTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeTotalToken:
		v, _ := costs.TotalTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeReasoningToken:
		v, _ := costs.ReasoningTokens()
		cost = uint64(v)
	case filterapi.LLMRequestCostTypeCEL:
		var err error

		in, _ := costs.InputTokens()
		cachedIn, _ := costs.CachedInputTokens()
		cacheCreation, _ := costs.CacheCreationInputTokens()
		out, _ := costs.OutputTokens()
		total, _ := costs.TotalTokens()
		reasoning, _ := costs.ReasoningTokens()
		cost, err = llmcostcel.EvaluateProgram(
			celProg,
			requestHeaders[internalapi.ModelNameHeaderKeyDefault],
			backendName,
			routeName,
			in,
			cachedIn,
			cacheCreation,
			out,
			total,
			reasoning,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to evaluate CEL expression: %w", err)
		}
	default:
		return 0, fmt.Errorf("unknown cost type: %s", costType)
	}
	return cost, nil
}

// evalRuntimeGlobalRequestCost computes the cost value for a single global runtime cost rule.
func evalRuntimeGlobalRequestCost(rc *filterapi.RuntimeGlobalRequestCost, costs *metrics.TokenUsage, requestHeaders map[string]string, backendName, routeName string) (uint64, error) {
	return evalCost(rc.Type, rc.CELProg, costs, requestHeaders, backendName, routeName)
}

// evalRuntimeRequestCost computes the cost value for a single route-scoped runtime cost rule.
func evalRuntimeRequestCost(rc *filterapi.RuntimeRequestCost, costs *metrics.TokenUsage, requestHeaders map[string]string, backendName, routeName string) (uint64, error) {
	return evalCost(rc.Type, rc.CELProg, costs, requestHeaders, backendName, routeName)
}

// buildDynamicMetadata creates metadata for rate limiting and cost tracking.
// This function is called by the upstream filter at the end of the stream (body.EndOfStream=true), and,
// for streaming responses, also as soon as a chunk carries new usage so the access log still captures it
// if the downstream client disconnects before EndOfStream is observed. It is not called for failed requests.
// The metadata includes token usage costs and model information for downstream processing.
// Two-tier precedence: for each metadataKey, check route-scoped requestCosts first (matching RouteName == routeName).
// If found, use it. Otherwise, fall back to globalRequestCosts. If neither exists, the key is not emitted.
func buildDynamicMetadata(globalRequestCosts []filterapi.RuntimeGlobalRequestCost, requestCosts []filterapi.RuntimeRequestCost, costs *metrics.TokenUsage, requestHeaders map[string]string, backendName, routeName, responseModel string) (*structpb.Struct, error) {
	metadata := make(map[string]*structpb.Value, len(requestCosts)+len(globalRequestCosts)+3)

	// Track which metadata keys have been populated by route-scoped costs.
	populatedKeys := make(map[string]struct{})

	shortBackend := backendName
	if parts := strings.SplitN(backendName, "/", 3); len(parts) >= 2 {
		shortBackend = parts[0] + "/" + parts[1]
	}

	actualModel := requestHeaders[internalapi.ModelNameHeaderKeyDefault]

	// First, process route-scoped costs that match this route.
	// Route-scoped costs must have a RouteName set (validated at runtime config creation).
	for i := range requestCosts {
		rc := &requestCosts[i]
		if rc.Backend != "" && rc.Backend != shortBackend {
			continue
		}
		if rc.RouteName != routeName {
			continue
		}
		if rc.Model != "" && rc.Model != actualModel {
			continue
		}
		cost, err := evalRuntimeRequestCost(rc, costs, requestHeaders, backendName, routeName)
		if err != nil {
			return nil, err
		}
		metadata[rc.MetadataKey] = &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: float64(cost)}}
		populatedKeys[rc.MetadataKey] = struct{}{}
	}

	// Then, process global costs for keys not already populated.
	for i := range globalRequestCosts {
		rc := &globalRequestCosts[i]
		if _, exists := populatedKeys[rc.MetadataKey]; exists {
			continue // Route-scoped cost already set this key.
		}
		cost, err := evalRuntimeGlobalRequestCost(rc, costs, requestHeaders, backendName, routeName)
		if err != nil {
			return nil, err
		}
		metadata[rc.MetadataKey] = &structpb.Value{Kind: &structpb.Value_NumberValue{NumberValue: float64(cost)}}
	}

	metadata["model_name_override"] = &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: actualModel}}

	if backendName != "" {
		// backend_name itself is emitted by buildBackendDynamicMetadata in the request
		// headers phase, so it is already on the stream by the time this runs.
		//
		// ai_service_backend_name stores the short "namespace/name" format extracted
		// from the full PerRouteRuleRefBackendName ("{namespace}/{name}/route/...").
		// This is used by the quota rate limit descriptor actions to match the
		// rate limit service config which keys on "namespace/backendName".
		parts := strings.SplitN(backendName, "/", 3)
		shortName := backendName
		if len(parts) >= 2 {
			shortName = parts[0] + "/" + parts[1]
		}
		metadata["ai_service_backend_name"] = &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: shortName}}
	}
	if routeName != "" {
		metadata["route_name"] = &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: routeName}}
	}

	// responseModel is the actual model that served the request.
	if responseModel != "" {
		metadata["response_model"] = &structpb.Value{Kind: &structpb.Value_StringValue{StringValue: responseModel}}
	}

	if len(metadata) == 0 {
		return nil, nil
	}

	return &structpb.Struct{
		Fields: map[string]*structpb.Value{
			internalapi.AIGatewayFilterMetadataNamespace: {
				Kind: &structpb.Value_StructValue{
					StructValue: &structpb.Struct{Fields: metadata},
				},
			},
		},
	}, nil
}
