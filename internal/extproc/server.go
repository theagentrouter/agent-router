// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/backendauth"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/redaction"
)

var (
	sensitiveHeaderRedactedValue = []byte("[REDACTED]")
	sensitiveHeaderKeys          = []string{"authorization", "x-api-key"}
)

// contextKey is a type for context keys to avoid collisions.
type contextKey string

// loggerContextKey is the context key for the request-scoped logger.
const loggerContextKey contextKey = "logger"

// loggerFromContext extracts the request-scoped logger from the context.
// If no logger is found in the context, it returns nil.
func loggerFromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerContextKey).(*slog.Logger); ok {
		return logger
	}
	return nil
}

// Server implements the external processor server.
type Server struct {
	logger                        *slog.Logger
	debugLogEnabled               bool
	enableRedaction               bool
	config                        *filterapi.RuntimeConfig
	processorFactories            map[string]ProcessorFactory
	prefixProcessorFactories      []prefixProcessorFactory
	routerProcessorsPerReqID      map[string]Processor
	routerProcessorsPerReqIDMutex sync.RWMutex
	uuidFn                        func() string
}

// prefixProcessorFactory associates a path prefix with a processor factory. It is used for
// endpoints whose paths contain variable segments (e.g. the Files API "/v1/files/{id}"),
// which cannot be matched by the exact-path map.
type prefixProcessorFactory struct {
	prefix  string
	factory ProcessorFactory
}

// NewServer creates a new external processor server.
func NewServer(logger *slog.Logger, enableRedaction bool) (*Server, error) {
	debugLogEnabled := logger.Enabled(context.Background(), slog.LevelDebug)
	srv := &Server{
		logger:                   logger,
		debugLogEnabled:          debugLogEnabled,
		enableRedaction:          enableRedaction,
		processorFactories:       make(map[string]ProcessorFactory),
		routerProcessorsPerReqID: make(map[string]Processor),
		uuidFn:                   uuid.NewString,
	}
	return srv, nil
}

// LoadConfig updates the configuration of the external processor.
func (s *Server) LoadConfig(ctx context.Context, config *filterapi.Config) error {
	newConfig, err := filterapi.NewRuntimeConfig(ctx, config, backendauth.NewHandler)
	if err != nil {
		return fmt.Errorf("cannot create runtime filter config: %w", err)
	}
	s.config = newConfig // This is racey, but we don't care.
	return nil
}

// Register a new processor for the given request path.
func (s *Server) Register(path string, newProcessor ProcessorFactory) {
	s.logger.Info("Registering processor", slog.String("path", path))
	s.processorFactories[path] = newProcessor
}

// RegisterPrefix registers a processor for any request path equal to prefix or nested under
// it (i.e. starting with prefix+"/"). Exact-path registrations take precedence, and the
// longest matching prefix wins. This supports endpoints with variable path segments such as
// the Files API ("/v1/files", "/v1/files/{id}", "/v1/files/{id}/content").
func (s *Server) RegisterPrefix(prefix string, newProcessor ProcessorFactory) {
	s.logger.Info("Registering prefix processor", slog.String("prefix", prefix))
	s.prefixProcessorFactories = append(s.prefixProcessorFactories, prefixProcessorFactory{prefix: prefix, factory: newProcessor})
}

var errNoProcessor = errors.New("no processor registered for the given path")

// processorForPath returns the processor for the given path.
// Exact path matching is tried first; if no exact match exists, the longest registered
// prefix (see RegisterPrefix) that matches the path on a segment boundary is used.
func (s *Server) processorForPath(requestHeaders map[string]string, isUpstreamFilter bool, logger *slog.Logger) (Processor, error) {
	pathHeader := ":path"
	if isUpstreamFilter {
		pathHeader = originalPathHeader
	}
	path := requestHeaders[pathHeader]

	// Strip query parameters for processor lookup.
	if queryIndex := strings.Index(path, "?"); queryIndex != -1 {
		path = path[:queryIndex]
	}

	if newProcessor, ok := s.processorFactories[path]; ok {
		return newProcessor(s.config, requestHeaders, logger, isUpstreamFilter, s.enableRedaction)
	}

	// Fall back to the longest matching prefix registration.
	var best ProcessorFactory
	bestLen := -1
	for i := range s.prefixProcessorFactories {
		pf := &s.prefixProcessorFactories[i]
		if (path == pf.prefix || strings.HasPrefix(path, pf.prefix+"/")) && len(pf.prefix) > bestLen {
			best = pf.factory
			bestLen = len(pf.prefix)
		}
	}
	if best != nil {
		return best(s.config, requestHeaders, logger, isUpstreamFilter, s.enableRedaction)
	}
	return nil, fmt.Errorf("%w: %s", errNoProcessor, path)
}

// originalPathHeader is the header used to pass the original path to the processor.
// This is used in the upstream filter level to determine the original path of the request on retry.
const originalPathHeader = internalapi.OriginalPathHeader

// internalReqIDHeader is the header used to pass the unique internal request ID to the upstream filter.
// This ensures that the upstream filter uses the same unique ID as the router filter to avoid race conditions.
const internalReqIDHeader = internalapi.EnvoyAIGatewayHeaderPrefix + "internal-req-id"

// Process implements [extprocv3.ExternalProcessorServer].
func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	ctx := stream.Context()
	if s.debugLogEnabled {
		s.logger.Debug("handling a new stream", slog.Any("config_uuid", s.config.UUID))
	}

	// The processor will be instantiated when the first message containing the request headers is received.
	// The :path header is used to determine the processor to use, based on the registered ones.
	//
	// If this extproc filter is invoked without going through a RequestHeaders phase, that means
	// an earlier filter has already processed the request headers/bodies and decided to terminate
	// the request by sending an immediate response. In this case, we will use the passThroughProcessor
	// to pass the request through without any processing as there would be nothing to process from AI Gateway's perspective.
	var p Processor = passThroughProcessor{}
	var isUpstreamFilter bool
	var internalReqID string
	var originalReqID string
	var logger *slog.Logger
	// Seed the context with the server-level logger as a fallback so that loggerFromContext never returns nil in processMsg.
	ctx = context.WithValue(ctx, loggerContextKey, s.logger)
	defer func() {
		if !isUpstreamFilter {
			s.routerProcessorsPerReqIDMutex.Lock()
			defer s.routerProcessorsPerReqIDMutex.Unlock()
			delete(s.routerProcessorsPerReqID, internalReqID)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := stream.Recv()
		if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
			return nil
		} else if err != nil {
			s.logger.Error("cannot receive stream request", slog.String("error", err.Error()))
			return status.Errorf(codes.Unknown, "cannot receive stream request: %v", err)
		}

		// If we're processing the request headers, read the :path header to instantiate the
		// right processor.
		// Note that `req.GetRequestHeaders()` will only return non-nil if the request is
		// of type `ProcessingRequest_RequestHeaders`, so this will be executed only once per
		// request, and the processor will be instantiated only once.
		if headers := req.GetRequestHeaders().GetHeaders(); headers != nil {
			headersMap := headersToMap(headers)
			originalReqID = headersMap["x-request-id"]
			// Assume that when attributes are set, this stream is for the upstream filter level.
			isUpstreamFilter = req.GetAttributes() != nil

			if isUpstreamFilter {
				// For upstream filter, use the internal request ID passed from the router filter
				internalReqID = headersMap[internalReqIDHeader]
				if internalReqID == "" {
					return status.Errorf(codes.Internal, "missing internal request ID header from router filter")
				}
				upstreamHostAttrs := req.GetAttributes()["envoy.filters.http.ext_proc"]
				setUpstreamHostAttributes(headersMap, upstreamHostAttrs)
			} else {
				// For router filter, create a unique internal request ID to avoid race conditions
				// with duplicate x-request-id values by appending a UUID suffix to the original request ID
				internalReqID = originalReqID + "-" + s.uuidFn()
			}

			// Create request-scoped logger with request_id before creating processor
			// so that the logger passed to translators includes the request_id field.
			if logger == nil {
				logger = s.logger.With("request_id", originalReqID, "is_upstream_filter", isUpstreamFilter)
			}
			// Add logger to context so processMsg can access it
			ctx = context.WithValue(ctx, loggerContextKey, logger)

			p, err = s.processorForPath(headersMap, isUpstreamFilter, logger)
			if err != nil {
				if errors.Is(err, errNoProcessor) {
					path := headersMap[":path"]
					_ = stream.Send(&extprocv3.ProcessingResponse{
						Response: &extprocv3.ProcessingResponse_ImmediateResponse{
							ImmediateResponse: &extprocv3.ImmediateResponse{
								Status:     &typev3.HttpStatus{Code: typev3.StatusCode_NotFound},
								Body:       fmt.Appendf(nil, "unsupported path: %s", path),
								GrpcStatus: &extprocv3.GrpcStatus{Status: uint32(codes.NotFound)},
							},
						},
					})
					return status.Errorf(codes.NotFound, "unsupported path: %s", path)
				}
				s.logger.Error("cannot get processor", slog.String("error", err.Error()))
				return status.Error(codes.NotFound, err.Error())
			}
			_, isEndpoinPicker := headersMap[internalapi.EndpointPickerHeaderKey]
			if isUpstreamFilter {
				if err = s.setBackend(ctx, p, internalReqID, isEndpoinPicker, req); err != nil {
					s.logger.Error("error processing request message", slog.String("error", err.Error()))
					return status.Errorf(codes.Unknown, "error processing request message: %v", err)
				}
			} else {
				s.routerProcessorsPerReqIDMutex.Lock()
				s.routerProcessorsPerReqID[internalReqID] = p
				s.routerProcessorsPerReqIDMutex.Unlock()
			}
		}

		// At this point, p is guaranteed to be a valid processor either from the concrete processor or the passThroughProcessor.
		resp, err := s.processMsg(ctx, p, req, internalReqID, isUpstreamFilter)
		if err != nil {
			s.logger.Error("error processing request message", slog.String("error", err.Error()))
			return status.Errorf(codes.Unknown, "error processing request message: %v", err)
		}
		if err := stream.Send(resp); err != nil {
			s.logger.Error("cannot send response", slog.String("error", err.Error()))
			return status.Errorf(codes.Unknown, "cannot send response: %v", err)
		}
	}
}

func (s *Server) processMsg(ctx context.Context, p Processor, req *extprocv3.ProcessingRequest, internalReqID string, isUpstreamFilter bool) (*extprocv3.ProcessingResponse, error) {
	l := loggerFromContext(ctx)
	switch value := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		requestHdrs := req.GetRequestHeaders().Headers
		// If DEBUG log level is enabled, filter sensitive headers before logging.
		if s.debugLogEnabled {
			filteredHdrs := filterSensitiveHeadersForLogging(requestHdrs, sensitiveHeaderKeys)
			l.Debug("request headers processing", slog.Any("request_headers", filteredHdrs))
		}
		// Thread Envoy dynamic metadata into context so credential override handlers
		// can read it without changing the BackendAuthHandler interface.
		ctx = backendauth.WithEnvoyMetadata(ctx, req.GetMetadataContext())
		resp, err := p.ProcessRequestHeaders(ctx, requestHdrs)
		if err != nil {
			return nil, fmt.Errorf("cannot process request headers: %w", err)
		}

		// For router filter, inject the internal request ID header so upstream filter can use it
		if !isUpstreamFilter && resp != nil {
			if requestHeaders, ok := resp.Response.(*extprocv3.ProcessingResponse_RequestHeaders); ok {
				// Ensure we have header mutation to add the internal request ID
				if requestHeaders.RequestHeaders == nil {
					requestHeaders.RequestHeaders = &extprocv3.HeadersResponse{}
				}
				if requestHeaders.RequestHeaders.Response == nil {
					requestHeaders.RequestHeaders.Response = &extprocv3.CommonResponse{}
				}
				if requestHeaders.RequestHeaders.Response.HeaderMutation == nil {
					requestHeaders.RequestHeaders.Response.HeaderMutation = &extprocv3.HeaderMutation{}
				}

				// Add the internal request ID header
				internalReqIDHeaderValue := &corev3.HeaderValueOption{
					Header: &corev3.HeaderValue{
						Key:      internalReqIDHeader,
						RawValue: []byte(internalReqID),
					},
				}
				requestHeaders.RequestHeaders.Response.HeaderMutation.SetHeaders = append(
					requestHeaders.RequestHeaders.Response.HeaderMutation.SetHeaders,
					internalReqIDHeaderValue,
				)
			}
		}
		if s.debugLogEnabled && resp != nil && resp.Response != nil {
			// Header mutations always carry injected backend credentials (e.g. the
			// upstream Authorization), so they are redacted whenever debug logging
			// is on — independent of enableRedaction, which only controls body
			// content. This mirrors redactRequestBodyResponse above.
			var logContent any
			switch val := resp.Response.(type) {
			case *extprocv3.ProcessingResponse_RequestHeaders:
				logContent = redactProcessingResponseRequestHeaders(val, s.logger, sensitiveHeaderKeys, s.enableRedaction)
			case *extprocv3.ProcessingResponse_ImmediateResponse:
				logContent = val
			}
			l.Debug("request headers processed", slog.Any("response", logContent))
		}
		return resp, nil
	case *extprocv3.ProcessingRequest_RequestBody:
		if s.debugLogEnabled && !s.enableRedaction {
			l.Debug("request body processing", slog.Any("request", req))
		}
		resp, err := p.ProcessRequestBody(ctx, value.RequestBody)
		if err != nil {
			return nil, fmt.Errorf("cannot process request body: %w", err)
		}
		// If the DEBUG log level is enabled, filter the sensitive data before logging.
		if s.debugLogEnabled && resp != nil && resp.Response != nil {
			switch val := resp.Response.(type) {
			case *extprocv3.ProcessingResponse_RequestBody:
				logContent := redactRequestBodyResponse(val, l, sensitiveHeaderKeys, s.enableRedaction)
				l.Debug("request body processed", slog.Any("response", logContent))
			case *extprocv3.ProcessingResponse_ImmediateResponse:
				// ImmediateResponse (e.g., for malformed requests) doesn't need request body redaction
				l.Debug("request body processed", slog.Any("response", val))
			}
		}
		return resp, nil
	case *extprocv3.ProcessingRequest_ResponseHeaders:
		responseHdrs := req.GetResponseHeaders().Headers
		if s.debugLogEnabled {
			l.Debug("response headers processing", slog.Any("response_headers", responseHdrs))
		}
		resp, err := p.ProcessResponseHeaders(ctx, responseHdrs)
		if err != nil {
			return nil, fmt.Errorf("cannot process response headers: %w", err)
		}
		if s.debugLogEnabled {
			l.Debug("response headers processed", slog.Any("response", resp))
		}
		return resp, nil
	case *extprocv3.ProcessingRequest_ResponseBody:
		if s.debugLogEnabled && !s.enableRedaction {
			l.Debug("response body processing", slog.Any("request", req))
		}
		resp, err := p.ProcessResponseBody(ctx, value.ResponseBody)
		if err != nil {
			return nil, fmt.Errorf("cannot process response body: %w", err)
		}

		// If the DEBUG log level is enabled, filter the sensitive data before logging.
		if s.debugLogEnabled && resp != nil && resp.Response != nil {
			var logContent any
			if s.enableRedaction {
				switch val := resp.Response.(type) {
				case *extprocv3.ProcessingResponse_ResponseBody:
					logContent = redactResponseBodyResponseFull(val, l, sensitiveHeaderKeys)
				case *extprocv3.ProcessingResponse_ImmediateResponse:
					logContent = val
				}
			} else {
				logContent = resp
			}
			l.Debug("response body processed", slog.Any("response", logContent))
		}

		return resp, nil
	default:
		l.Error("unknown request type", slog.Any("request", value))
		return nil, fmt.Errorf("unknown request type: %T", value)
	}
}

// setBackend retrieves the backend from the request attributes and sets it in the processor. This is only called
// if the processor is an upstream filter.
func (s *Server) setBackend(ctx context.Context, p Processor, internalReqID string, isEndpointPicker bool, req *extprocv3.ProcessingRequest) error {
	attributes := req.GetAttributes()["envoy.filters.http.ext_proc"]
	if attributes == nil || len(attributes.Fields) == 0 { // coverage-ignore
		return status.Error(codes.Internal, "missing attributes in request")
	}

	backendName, err := resolveBackendName(isEndpointPicker, attributes)
	if err != nil {
		return err
	}
	routeName := resolveRouteName(attributes)

	backend, ok := s.config.Backends[backendName]
	if !ok {
		return status.Errorf(codes.Internal, "unknown backend: %s", backendName)
	}

	s.routerProcessorsPerReqIDMutex.RLock()
	defer s.routerProcessorsPerReqIDMutex.RUnlock()
	routerProcessor, ok := s.routerProcessorsPerReqID[internalReqID]
	if !ok {
		return status.Errorf(codes.Internal, "no router processor found, request_id=%s, backend=%s",
			internalReqID, backendName)
	}

	if err := p.SetBackend(ctx, backend, routeName, routerProcessor); err != nil {
		return status.Errorf(codes.Internal, "cannot set backend: %v", err)
	}
	return nil
}

func resolveBackendName(isEndpointPicker bool, attributes *structpb.Struct) (string, error) {
	var backendNamePath string
	if isEndpointPicker {
		backendNamePath = internalapi.XDSClusterMetadataBackendNamePath
	} else {
		backendNamePath = internalapi.XDSUpstreamHostMetadataBackendNamePath
	}

	// Try the direct metadata path first. (e.g. xds.upstream_host_metadata...['per_route_rule_backend_name'])
	if b, ok := attributes.Fields[backendNamePath]; ok {
		return b.GetStringValue(), nil
	}

	// Fallback to cluster metadata when upstream host metadata is unavailable.
	if !isEndpointPicker {
		if b, ok := attributes.Fields[internalapi.XDSClusterMetadataBackendNamePath]; ok {
			return b.GetStringValue(), nil
		}
	}

	return "", status.Errorf(codes.Internal, "missing backend name in attributes at path: %s", backendNamePath)
}

func resolveRouteName(attributes *structpb.Struct) string {
	if routeName, ok := attributes.Fields[internalapi.XDSRouteMetadataRouteNamePath]; ok {
		return routeName.GetStringValue()
	}
	// Route metadata is not always available (e.g. legacy dataplane configs).
	// Keep request processing working and let CEL expressions decide behavior
	// when route_name is empty.
	return ""
}

func setUpstreamHostAttributes(headers map[string]string, attributes *structpb.Struct) {
	// The upstream-host header is an internal, controller-derived value. A downstream client could also
	// send it, so drop any inbound copy before overlaying trusted xDS metadata — otherwise, for an
	// endpoint with no derivable metadata (e.g. EDS), the client-supplied value would survive and drive
	// the AWS handler's SigV4 host.
	delete(headers, internalapi.UpstreamHostHeader)

	if attributes == nil {
		return
	}

	if v, ok := attributes.Fields[internalapi.XDSUpstreamHostMetadataUpstreamHostPath]; ok {
		if host := v.GetStringValue(); host != "" {
			headers[internalapi.UpstreamHostHeader] = host
		}
	}
}

// Check implements [grpc_health_v1.HealthServer].
func (s *Server) Check(context.Context, *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	return &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}, nil
}

// Watch implements [grpc_health_v1.HealthServer].
func (s *Server) Watch(*grpc_health_v1.HealthCheckRequest, grpc_health_v1.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "Watch is not implemented")
}

// List implements [grpc_health_v1.HealthServer].
func (s *Server) List(context.Context, *grpc_health_v1.HealthListRequest) (*grpc_health_v1.HealthListResponse, error) {
	return &grpc_health_v1.HealthListResponse{Statuses: map[string]*grpc_health_v1.HealthCheckResponse{
		"extproc": {Status: grpc_health_v1.HealthCheckResponse_SERVING},
	}}, nil
}

// awsCredentialHeaderSuffixes are the tails of the three per-request SigV4 credential headers.
// The prefix is configurable, so match on the suffix: otherwise a prefix outside x-aigw- (say
// "x-aws-", to keep an existing injector unchanged) leaks the secret access key into debug logs.
var awsCredentialHeaderSuffixes = []string{"-access-key-id", "-secret-access-key", "-session-token"}

// isSensitiveHeader reports whether the header should be redacted in logs. Beyond the static
// sensitiveHeaderKeys list: anything under "x-aigw-", which carries per-request credential
// overrides, and any AWS SigV4 credential part under any prefix.
func isSensitiveHeader(key string, sensitiveKeys []string) bool {
	lower := strings.ToLower(key)
	if slices.Contains(sensitiveKeys, lower) || strings.HasPrefix(lower, "x-aigw-") {
		return true
	}
	for _, suffix := range awsCredentialHeaderSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// filterSensitiveHeadersForLogging filters out sensitive headers from the provided HeaderMap for logging.
// Specifically, it redacts the value of the "authorization" header and logs this action.
// This returns a slice of [slog.Attr] of headers, where the value of sensitive headers is redacted.
func filterSensitiveHeadersForLogging(headers *corev3.HeaderMap, sensitiveKeys []string) []slog.Attr {
	if headers == nil {
		return nil
	}
	filteredHeaders := make([]slog.Attr, len(headers.Headers))
	for i, header := range headers.Headers {
		// We convert the header key to lowercase to make the comparison case-insensitive but we don't modify the original header.
		if isSensitiveHeader(header.GetKey(), sensitiveKeys) {
			filteredHeaders[i] = slog.String(header.GetKey(), string(sensitiveHeaderRedactedValue))
		} else {
			if len(header.Value) > 0 {
				filteredHeaders[i] = slog.String(header.GetKey(), header.Value)
			} else if utf8.Valid(header.RawValue) {
				filteredHeaders[i] = slog.String(header.GetKey(), string(header.RawValue))
			}
		}
	}
	return filteredHeaders
}

// redactProcessingResponseRequestHeaders creates a safe-to-log copy of the request headers processing response.
// Used exclusively for debug logging without modifying the actual response sent to Envoy.
//
// Header mutations are always redacted (API keys, authorization tokens) — a backend
// credential injected here must never reach the debug log, regardless of the
// [Server.enableRedaction] flag, which only governs the more expensive body-content
// redaction. When redactBody is false, the body mutation is logged as-is (mirroring
// [redactRequestBodyResponse]); when true, it is redacted too.
func redactProcessingResponseRequestHeaders(resp *extprocv3.ProcessingResponse_RequestHeaders, logger *slog.Logger, sensitiveKeys []string, redactBody bool) *extprocv3.ProcessingResponse_RequestHeaders {
	if resp == nil || resp.RequestHeaders == nil || resp.RequestHeaders.Response == nil {
		return &extprocv3.ProcessingResponse_RequestHeaders{}
	}
	originalHeaderMutation := resp.RequestHeaders.GetResponse().GetHeaderMutation()
	var bodyMutation *extprocv3.BodyMutation
	if redactBody {
		bodyMutation = redactBodyMutation(resp.RequestHeaders.Response.GetBodyMutation())
	} else {
		bodyMutation = resp.RequestHeaders.Response.GetBodyMutation()
	}

	return &extprocv3.ProcessingResponse_RequestHeaders{
		RequestHeaders: &extprocv3.HeadersResponse{
			Response: &extprocv3.CommonResponse{
				HeaderMutation:  redactHeaderMutation(originalHeaderMutation, logger, sensitiveKeys),
				BodyMutation:    bodyMutation,
				ClearRouteCache: resp.RequestHeaders.Response.GetClearRouteCache(),
			},
		},
	}
}

// redactHeaderMutation creates a copy of header mutations with sensitive header values redacted.
// This is a helper function used by the redactProcessingResponse* functions.
//
// Sensitive headers (matched case-insensitively against sensitiveKeys) have their values
// replaced with [REDACTED] while preserving the header name. This allows debugging which
// headers were set without exposing API keys or tokens in logs.
func redactHeaderMutation(originalHeaderMutation *extprocv3.HeaderMutation, logger *slog.Logger, sensitiveKeys []string) *extprocv3.HeaderMutation {
	redactedHeaderMutation := &extprocv3.HeaderMutation{
		RemoveHeaders: originalHeaderMutation.GetRemoveHeaders(),
		SetHeaders:    make([]*corev3.HeaderValueOption, 0, len(originalHeaderMutation.GetSetHeaders())),
	}
	for _, setHeader := range originalHeaderMutation.GetSetHeaders() {
		// Convert header key to lowercase for case-insensitive matching (HTTP headers are case-insensitive)
		// but preserve the original casing in the redacted output for debugging
		if isSensitiveHeader(setHeader.Header.GetKey(), sensitiveKeys) {
			logger.Debug("filtering sensitive header", slog.String("header_key", setHeader.Header.Key))
			redactedHeaderMutation.SetHeaders = append(redactedHeaderMutation.SetHeaders, &corev3.HeaderValueOption{
				Header: &corev3.HeaderValue{
					Key:      setHeader.Header.Key,
					RawValue: sensitiveHeaderRedactedValue,
				},
			})
		} else {
			redactedHeaderMutation.SetHeaders = append(redactedHeaderMutation.SetHeaders, setHeader)
		}
	}
	return redactedHeaderMutation
}

// redactBodyMutation creates a redacted version of response body content for safe logging.
// Replaces the actual body with a placeholder containing length and hash information.
// The hash allows debugging cache hits/misses and correlating requests without exposing sensitive content.
//
// Format: [REDACTED LENGTH=n HASH=xxxxxxxx]
func redactBodyMutation(bodyMutation *extprocv3.BodyMutation) *extprocv3.BodyMutation {
	if bodyMutation == nil {
		return nil
	}

	switch m := bodyMutation.Mutation.(type) {
	case *extprocv3.BodyMutation_Body:
		if len(m.Body) == 0 {
			return bodyMutation
		}
		redactedBody := []byte(redaction.RedactString(string(m.Body)))
		return &extprocv3.BodyMutation{
			Mutation: &extprocv3.BodyMutation_Body{
				Body: redactedBody,
			},
		}
	case *extprocv3.BodyMutation_ClearBody:
		// ClearBody doesn't contain sensitive data, return as-is
		return bodyMutation
	default:
		return bodyMutation
	}
}

// redactResponseBodyResponseFull creates a safe-to-log copy with headers AND body redacted.
// This is used exclusively for debug logging when enableRedaction is true.
// The original response is never modified to ensure the actual AI provider response flows through unchanged.
// Both headers (API keys, auth tokens) and body content (AI-generated text, images, etc.) are redacted.
func redactResponseBodyResponseFull(resp *extprocv3.ProcessingResponse_ResponseBody, logger *slog.Logger, sensitiveKeys []string) *extprocv3.ProcessingResponse_ResponseBody {
	if resp == nil || resp.ResponseBody == nil || resp.ResponseBody.Response == nil {
		return &extprocv3.ProcessingResponse_ResponseBody{}
	}

	originalHeaderMutation := resp.ResponseBody.Response.GetHeaderMutation()
	originalBodyMutation := resp.ResponseBody.Response.GetBodyMutation()

	return &extprocv3.ProcessingResponse_ResponseBody{
		ResponseBody: &extprocv3.BodyResponse{
			Response: &extprocv3.CommonResponse{
				HeaderMutation:  redactHeaderMutation(originalHeaderMutation, logger, sensitiveKeys),
				BodyMutation:    redactBodyMutation(originalBodyMutation),
				ClearRouteCache: resp.ResponseBody.Response.GetClearRouteCache(),
			},
		},
	}
}

// redactRequestBodyResponse creates a safe-to-log copy of the request body response.
// When redactBody is false, only headers are filtered while body content is logged as-is for debugging.
// When redactBody is true, both headers (API keys, auth tokens) and body content are redacted for production-safe logging.
func redactRequestBodyResponse(resp *extprocv3.ProcessingResponse_RequestBody, logger *slog.Logger, sensitiveKeys []string, redactBody bool) *extprocv3.ProcessingResponse_RequestBody {
	if resp == nil || resp.RequestBody == nil || resp.RequestBody.Response == nil {
		return &extprocv3.ProcessingResponse_RequestBody{}
	}

	originalHeaderMutation := resp.RequestBody.Response.GetHeaderMutation()
	var bodyMutation *extprocv3.BodyMutation
	if redactBody {
		bodyMutation = redactBodyMutation(resp.RequestBody.Response.GetBodyMutation())
	} else {
		bodyMutation = resp.RequestBody.Response.GetBodyMutation()
	}

	return &extprocv3.ProcessingResponse_RequestBody{
		RequestBody: &extprocv3.BodyResponse{
			Response: &extprocv3.CommonResponse{
				HeaderMutation:  redactHeaderMutation(originalHeaderMutation, logger, sensitiveKeys),
				BodyMutation:    bodyMutation,
				ClearRouteCache: resp.RequestBody.Response.GetClearRouteCache(),
			},
		},
	}
}

// headersToMap converts a [corev3.HeaderMap] to a Go map for easier processing.
func headersToMap(headers *corev3.HeaderMap) map[string]string {
	// TODO: handle multiple headers with the same key.
	hdrs := make(map[string]string)
	for _, h := range headers.GetHeaders() {
		if len(h.Value) > 0 {
			hdrs[h.GetKey()] = h.Value
		} else if utf8.Valid(h.RawValue) {
			hdrs[h.GetKey()] = string(h.RawValue)
		}
	}
	return hdrs
}
