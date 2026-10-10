// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/envoyproxy/ai-gateway/internal/lang"
)

// nolint: godot
const (
	// MCP Request Duration is histogram metric that records the duration of MCP requests.
	//
	// Dimensions:
	// - error.type
	mcpRequestDuration = "mcp.request.duration"
	// MCP Method Count is a counter metric that records the total number of MCP methods invoked.
	//
	// Dimensions:
	// - mcp.method.name
	// - status
	mcpMethodCount = "mcp.method.count"
	// MCP Initialization Duration is a histogram metric that records the duration of MCP initialization.
	mcpInitializationDuration = "mcp.initialization.duration"
	// MCP Capabilities Negotiated is a counter metric that records the total number of MCP capabilities negotiated.
	//
	// Dimensions:
	// - capability.type
	// - capability.side
	mcpCapabilitiesNegotiated = "mcp.capabilities.negotiated"
	// MCP Progress Notifications is a counter metric that records the total number of MCP progress notifications sent.
	mpcProgressNotifications = "mcp.progress.notifications"
	// MCP Notification Stream Open Attempts is a counter metric that records the total number of legacy GET
	// notification streams the gateway tried to open to an upstream MCP backend. It is recorded when the request starts.
	//
	// Dimensions:
	// - mcp.backend
	mcpNotificationStreamOpenAttempts = "mcp.notification_stream.open.attempts"
	// MCP Notification Stream Open Outcomes is a counter metric that records how each legacy GET notification
	// stream open attempt ended. See MCPNotificationStreamOutcome for all outcomes.
	//
	// Dimensions:
	// - mcp.backend
	// - mcp.notification_stream.outcome
	mcpNotificationStreamOpenOutcomes = "mcp.notification_stream.open.outcomes"
	// MCP Notification Stream Active is an up/down counter metric that records the number of legacy GET
	// notification streams that are currently open to an upstream MCP backend.
	//
	// Dimensions:
	// - mcp.backend
	mcpNotificationStreamActive = "mcp.notification_stream.active"
	// MCP Notification Stream Duration is a histogram metric that records the lifetime in seconds of an opened
	// legacy GET notification stream, from when it opened until it ended. See MCPNotificationStreamEndReason
	// for all end reasons.
	//
	// Dimensions:
	// - mcp.backend
	// - mcp.notification_stream.end_reason
	mcpNotificationStreamDuration = "mcp.notification_stream.duration"
	// MCP JSON-RPC method name attribute.
	mcpAttributeMethodName = "mcp.method.name"
	// MCP status attribute, which is either "success" or "error". See mcpStatusType for all statuses.
	mcpAttributeStatusName = "status"
	// MCP error type attribute. See MCPErrorType for all error types.
	mcpAttributeErrorType = "error.type"
	// MCP capability type, which is for example, "tools" or "resources". See mcpCapabilityType for all types.
	mcpAttributeCapabilityType = "capability.type"
	// MCP capability side, which is either "client" or "server". See mcpCapabilitySide for all sides.
	mcpAttributeCapabilitySide = "capability.side"
	// MCP backend attribute, which identifies the upstream MCP backend that handled the request.
	mcpAttributeBackend = "mcp.backend"
	// MCP notification stream open outcome attribute. See MCPNotificationStreamOutcome for all outcomes.
	mcpAttributeStreamOutcome = "mcp.notification_stream.outcome"
	// MCP notification stream end reason attribute. See MCPNotificationStreamEndReason for all reasons.
	mcpAttributeStreamEndReason = "mcp.notification_stream.end_reason"
)

// MCPErrorType defines the type of error that occurred during an MCP request.
type MCPErrorType string

const (
	// MCPErrorUnsupportedProtocolVersion indicates that the protocol version is not supported.
	MCPErrorUnsupportedProtocolVersion MCPErrorType = "unsupported_protocol_version"
	// MCPErrorInvalidJSONRPC indicates that the JSON-RPC request is invalid.
	MCPErrorInvalidJSONRPC MCPErrorType = "invalid_json_rpc"
	// MCPErrorUnsupportedMethod indicates that the method is not supported.
	MCPErrorUnsupportedMethod MCPErrorType = "unsupported_method"
	// MCPErrorUnsupportedResponse indicates that the response is not supported.
	MCPErrorUnsupportedResponse MCPErrorType = "unsupported_response"
	// MCPErrorInvalidParam indicates that a parameter is invalid.
	MCPErrorInvalidParam MCPErrorType = "invalid_param"
	// MCPErrorInvalidSessionID indicates that the session ID is invalid.
	MCPErrorInvalidSessionID MCPErrorType = "invalid_session_id"
	// MCPErrorInternal indicates that an internal error occurred.
	MCPErrorInternal MCPErrorType = "internal_error"
)

// MCPStatusType defines the status of an MCP request.
type MCPStatusType string

const (
	MCPStatusSuccess MCPStatusType = "success"
	MCPStatusFailed  MCPStatusType = "failed"
	MCPStatusError   MCPStatusType = "error"
)

// MCPNotificationStreamOutcome defines the outcome of an attempt to open a legacy GET notification stream
// to an upstream MCP backend.
type MCPNotificationStreamOutcome string

const (
	// MCPNotificationStreamOutcomeOpened indicates that the backend accepted the request and the stream is open.
	MCPNotificationStreamOutcomeOpened MCPNotificationStreamOutcome = "opened"
	// MCPNotificationStreamOutcomeUnsupported indicates that the backend does not offer a notification stream,
	// i.e. it returned 202, 204 or 405, or answered with a single JSON-RPC message instead of a stream.
	MCPNotificationStreamOutcomeUnsupported MCPNotificationStreamOutcome = "unsupported"
	// MCPNotificationStreamOutcomeHTTP4xx indicates that the backend rejected the request with a 4xx status code
	// other than 405.
	MCPNotificationStreamOutcomeHTTP4xx MCPNotificationStreamOutcome = "http_4xx"
	// MCPNotificationStreamOutcomeHTTP5xx indicates that the backend failed the request with a 5xx status code.
	MCPNotificationStreamOutcomeHTTP5xx MCPNotificationStreamOutcome = "http_5xx"
	// MCPNotificationStreamOutcomeHTTPOther indicates that the backend returned an unexpected status code
	// outside the 4xx and 5xx classes, e.g. 201 or 3xx.
	MCPNotificationStreamOutcomeHTTPOther MCPNotificationStreamOutcome = "http_other"
	// MCPNotificationStreamOutcomeTransportError indicates that the request failed before a usable response
	// was received, e.g. a connection error or an undecodable response body.
	MCPNotificationStreamOutcomeTransportError MCPNotificationStreamOutcome = "transport_error"
	// MCPNotificationStreamOutcomeCancelled indicates that the request was cancelled before the stream opened.
	MCPNotificationStreamOutcomeCancelled MCPNotificationStreamOutcome = "cancelled"
)

// MCPNotificationStreamEndReason defines why an opened legacy GET notification stream ended.
type MCPNotificationStreamEndReason string

const (
	// MCPNotificationStreamEndReasonEOF indicates that the backend closed the stream normally.
	MCPNotificationStreamEndReasonEOF MCPNotificationStreamEndReason = "eof"
	// MCPNotificationStreamEndReasonCancelled indicates that the stream was cancelled, e.g. the client disconnected.
	MCPNotificationStreamEndReasonCancelled MCPNotificationStreamEndReason = "cancelled"
	// MCPNotificationStreamEndReasonError indicates that the stream ended with an error, including an
	// unexpected EOF when the connection was cut mid-event.
	MCPNotificationStreamEndReasonError MCPNotificationStreamEndReason = "error"
)

// mcpCapabilityType defines the type of capability that is negotiated between client and server.
type mcpCapabilityType string

const (
	mcpCapabilityTypeTools        mcpCapabilityType = "tools"
	mcpCapabilityTypeResources    mcpCapabilityType = "resources"
	mcpCapabilityTypePrompts      mcpCapabilityType = "prompts"
	mcpCapabilityTypeSampling     mcpCapabilityType = "sampling"
	mcpCapabilityTypeRoots        mcpCapabilityType = "roots"
	mcpCapabilityTypeExperimental mcpCapabilityType = "experimental"
	mcpCapabilityTypeElicitation  mcpCapabilityType = "elicitation"
	mcpCapabilityTypeCompletions  mcpCapabilityType = "completions"
	mcpCapabilityTypeLogging      mcpCapabilityType = "logging"
)

// mcpCapabilitySide defines whether the capability is from the client or server.
type mcpCapabilitySide string

const (
	mcpCapabilitySideClient mcpCapabilitySide = "client"
	mcpCapabilitySideServer mcpCapabilitySide = "server"
)

// MCPMetrics holds metrics for MCP.
type MCPMetrics interface {
	// WithRequestAttributes returns a new MCPMetrics instance with default attributes extracted from the HTTP request.
	WithRequestAttributes(req *http.Request) MCPMetrics
	// WithBackend returns a new MCPMetrics instance with the backend attribute set.
	// This allows metrics to be filtered/sorted by the upstream MCP backend that handled the request.
	WithBackend(backend string) MCPMetrics
	// RecordRequestDuration records the duration of a success MCP request.
	RecordRequestDuration(ctx context.Context, startAt time.Time, meta mcpsdk.Params)
	// RecordRequestErrorDuration records the duration of an MCP request that resulted in an error.
	RecordRequestErrorDuration(ctx context.Context, startAt time.Time, errType MCPErrorType, meta mcpsdk.Params)
	// RecordMethodCount records the count of method invocations.
	RecordMethodCount(ctx context.Context, methodName string, meta mcpsdk.Params)
	// RecordMethodErrorCount records the count of method invocations with error status.
	RecordMethodErrorCount(ctx context.Context, methodName string, meta mcpsdk.Params, status MCPStatusType)
	// RecordInitializationDuration records the duration of MCP initialization.
	RecordInitializationDuration(ctx context.Context, startAt time.Time, meta mcpsdk.Params)
	// RecordClientCapabilities records the negotiated client capabilities.
	RecordClientCapabilities(ctx context.Context, capabilities *mcpsdk.ClientCapabilities, meta mcpsdk.Params)
	// RecordServerCapabilities records the negotiated server capabilities.
	RecordServerCapabilities(ctx context.Context, capabilities *mcpsdk.ServerCapabilities, meta mcpsdk.Params)
	// RecordProgress records a progress notification sent/received.
	RecordProgress(ctx context.Context, meta mcpsdk.Params)
	// RecordNotificationStreamOpenAttempt records the start of an attempt to open a legacy GET notification stream.
	RecordNotificationStreamOpenAttempt(ctx context.Context)
	// RecordNotificationStreamOpenOutcome records the outcome of an attempt to open a legacy GET notification stream.
	// The opened outcome also increments the number of active streams.
	RecordNotificationStreamOpenOutcome(ctx context.Context, outcome MCPNotificationStreamOutcome)
	// RecordNotificationStreamEnd records the end of a legacy GET notification stream that opened at openedAt.
	// It decrements the number of active streams and records the stream lifetime.
	RecordNotificationStreamEnd(ctx context.Context, openedAt time.Time, reason MCPNotificationStreamEndReason)
}

type mcp struct {
	requestDuration               metric.Float64Histogram
	methodCount                   metric.Float64Counter
	initializationDuration        metric.Float64Histogram
	capabilitiesNegotiated        metric.Float64Counter
	progressNotifications         metric.Float64Counter
	streamOpenAttempts            metric.Float64Counter
	streamOpenOutcomes            metric.Float64Counter
	streamActive                  metric.Float64UpDownCounter
	streamDuration                metric.Float64Histogram
	requestHeaderAttributeMapping map[string]string // maps HTTP headers to metric attribute names.
	defaultAttributes             []attribute.KeyValue
}

// NewMCP creates a new mcp metrics instance.
func NewMCP(meter metric.Meter, requestHeaderAttributeMapping map[string]string) MCPMetrics {
	return &mcp{
		requestHeaderAttributeMapping: requestHeaderAttributeMapping,
		requestDuration: mustRegisterHistogram(meter,
			mcpRequestDuration,
			metric.WithDescription("Duration of MCP requests"),
			metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10)),
		methodCount: mustRegisterCounter(
			meter,
			mcpMethodCount,
			metric.WithDescription("Total number of MCP methods invoked"),
		),
		initializationDuration: mustRegisterHistogram(meter,
			mcpInitializationDuration,
			metric.WithDescription("Duration of MCP initialization"),
			metric.WithUnit("token"),
			metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
		),
		capabilitiesNegotiated: mustRegisterCounter(
			meter,
			mcpCapabilitiesNegotiated,
			metric.WithDescription("Total number of MCP capabilities negotiated"),
		),
		progressNotifications: mustRegisterCounter(
			meter,
			mpcProgressNotifications,
			metric.WithDescription("Total number of MCP progress notifications sent"),
		),
		streamOpenAttempts: mustRegisterCounter(
			meter,
			mcpNotificationStreamOpenAttempts,
			metric.WithDescription("Total number of attempts to open an MCP backend notification stream"),
		),
		streamOpenOutcomes: mustRegisterCounter(
			meter,
			mcpNotificationStreamOpenOutcomes,
			metric.WithDescription("Total number of MCP backend notification stream open attempts by outcome"),
		),
		streamActive: mustRegisterUpDownCounter(
			meter,
			mcpNotificationStreamActive,
			metric.WithDescription("Number of currently open MCP backend notification streams"),
		),
		streamDuration: mustRegisterHistogram(meter,
			mcpNotificationStreamDuration,
			metric.WithDescription("Lifetime of MCP backend notification streams"),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.1, 1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200),
		),
	}
}

// WithBackend returns a new MCPMetrics instance with the backend attribute set.
func (m *mcp) WithBackend(backend string) MCPMetrics {
	withBackend := &mcp{
		requestDuration:               m.requestDuration,
		methodCount:                   m.methodCount,
		initializationDuration:        m.initializationDuration,
		capabilitiesNegotiated:        m.capabilitiesNegotiated,
		progressNotifications:         m.progressNotifications,
		streamOpenAttempts:            m.streamOpenAttempts,
		streamOpenOutcomes:            m.streamOpenOutcomes,
		streamActive:                  m.streamActive,
		streamDuration:                m.streamDuration,
		requestHeaderAttributeMapping: m.requestHeaderAttributeMapping,
		defaultAttributes: append(
			slices.Clone(m.defaultAttributes),
			attribute.String(mcpAttributeBackend, backend),
		),
	}
	return withBackend
}

// WithRequestAttributes returns a new MCPMetrics instance with default attributes extracted from
// the HTTP request headers.
func (m *mcp) WithRequestAttributes(req *http.Request) MCPMetrics {
	withAttrs := &mcp{
		requestDuration:               m.requestDuration,
		methodCount:                   m.methodCount,
		initializationDuration:        m.initializationDuration,
		capabilitiesNegotiated:        m.capabilitiesNegotiated,
		progressNotifications:         m.progressNotifications,
		streamOpenAttempts:            m.streamOpenAttempts,
		streamOpenOutcomes:            m.streamOpenOutcomes,
		streamActive:                  m.streamActive,
		streamDuration:                m.streamDuration,
		requestHeaderAttributeMapping: m.requestHeaderAttributeMapping,
	}

	for headerName, attrName := range m.requestHeaderAttributeMapping {
		if headerValue := req.Header.Get(headerName); headerValue != "" {
			withAttrs.defaultAttributes = append(
				withAttrs.defaultAttributes,
				attribute.String(attrName, headerValue),
			)
		}
	}

	return withAttrs
}

// RecordMethodCount implements [MCPMetrics.RecordMethodCount].
func (m *mcp) RecordMethodCount(ctx context.Context, methodName string, params mcpsdk.Params) {
	if methodName == "" {
		return
	}
	m.methodCount.Add(ctx, 1,
		m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeMethodName).String(methodName),
			attribute.String(mcpAttributeStatusName, string(MCPStatusSuccess)),
		))
}

// RecordMethodErrorCount implements [MCPMetrics.RecordMethodErrorCount].
func (m *mcp) RecordMethodErrorCount(ctx context.Context, methodName string, params mcpsdk.Params, status MCPStatusType) {
	if methodName == "" {
		return
	}
	m.methodCount.Add(ctx, 1,
		m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeMethodName).String(methodName),
			attribute.String(mcpAttributeStatusName, string(status)),
		))
}

// RecordRequestDuration implements [MCPMetrics.RecordRequestDuration].
func (m *mcp) RecordRequestDuration(ctx context.Context, startAt time.Time, params mcpsdk.Params) {
	duration := time.Since(startAt).Seconds()
	m.requestDuration.Record(ctx, duration, m.withDefaultAttributes(params))
}

// RecordRequestErrorDuration implements [MCPMetrics.RecordRequestErrorDuration].
func (m *mcp) RecordRequestErrorDuration(ctx context.Context, startAt time.Time, errType MCPErrorType, params mcpsdk.Params) {
	duration := time.Since(startAt).Seconds()
	m.requestDuration.Record(ctx, duration, m.withDefaultAttributes(params,
		attribute.Key(mcpAttributeErrorType).String(string(errType)),
	))
}

// RecordInitializationDuration implements [MCPMetrics.RecordInitializationDuration].
func (m *mcp) RecordInitializationDuration(ctx context.Context, startAt time.Time, params mcpsdk.Params) {
	duration := time.Since(startAt).Seconds()
	m.initializationDuration.Record(ctx, duration, m.withDefaultAttributes(params))
}

// RecordProgress implements [MCPMetrics.RecordProgress].
func (m *mcp) RecordProgress(ctx context.Context, params mcpsdk.Params) {
	m.progressNotifications.Add(ctx, 1, m.withDefaultAttributes(params))
}

// RecordNotificationStreamOpenAttempt implements [MCPMetrics.RecordNotificationStreamOpenAttempt].
func (m *mcp) RecordNotificationStreamOpenAttempt(ctx context.Context) {
	m.streamOpenAttempts.Add(ctx, 1, m.withDefaultAttributes(nil))
}

// RecordNotificationStreamOpenOutcome implements [MCPMetrics.RecordNotificationStreamOpenOutcome].
func (m *mcp) RecordNotificationStreamOpenOutcome(ctx context.Context, outcome MCPNotificationStreamOutcome) {
	m.streamOpenOutcomes.Add(ctx, 1, m.withDefaultAttributes(nil,
		attribute.Key(mcpAttributeStreamOutcome).String(string(outcome)),
	))
	if outcome == MCPNotificationStreamOutcomeOpened {
		m.streamActive.Add(ctx, 1, m.withDefaultAttributes(nil))
	}
}

// RecordNotificationStreamEnd implements [MCPMetrics.RecordNotificationStreamEnd].
func (m *mcp) RecordNotificationStreamEnd(ctx context.Context, openedAt time.Time, reason MCPNotificationStreamEndReason) {
	m.streamActive.Add(ctx, -1, m.withDefaultAttributes(nil))
	m.streamDuration.Record(ctx, time.Since(openedAt).Seconds(), m.withDefaultAttributes(nil,
		attribute.Key(mcpAttributeStreamEndReason).String(string(reason)),
	))
}

// RecordClientCapabilities implements [MCPMetrics.RecordClientCapabilities].
func (m *mcp) RecordClientCapabilities(ctx context.Context, capabilities *mcpsdk.ClientCapabilities, params mcpsdk.Params) {
	if capabilities == nil {
		return
	}

	side := string(mcpCapabilitySideClient)
	if l := len(capabilities.Experimental); l > 0 {
		m.capabilitiesNegotiated.Add(ctx, float64(l), m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeExperimental)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if capabilities.Roots.ListChanged {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeRoots)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if capabilities.Sampling != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeSampling)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if capabilities.Elicitation != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeElicitation)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}
}

// RecordServerCapabilities implements [MCPMetrics.RecordServerCapabilities].
func (m *mcp) RecordServerCapabilities(ctx context.Context, serverCapa *mcpsdk.ServerCapabilities, params mcpsdk.Params) {
	if serverCapa == nil {
		return
	}

	side := string(mcpCapabilitySideServer)
	if serverCapa.Completions != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeCompletions)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if l := len(serverCapa.Experimental); l > 0 {
		m.capabilitiesNegotiated.Add(ctx, float64(l), m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeExperimental)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if serverCapa.Logging != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeLogging)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if serverCapa.Prompts != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypePrompts)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if serverCapa.Resources != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeResources)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}

	if serverCapa.Tools != nil {
		m.capabilitiesNegotiated.Add(ctx, 1, m.withDefaultAttributes(params,
			attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeTools)),
			attribute.Key(mcpAttributeCapabilitySide).String(side),
		))
	}
}

// withDefaultAttributes merges defaults, request-mapped, and call-site attrs,
// preferring later values and emitting a stable key order.
func (m *mcp) withDefaultAttributes(params mcpsdk.Params, attrs ...attribute.KeyValue) metric.MeasurementOption {
	merged := make(map[attribute.Key]attribute.Value, len(m.defaultAttributes)+len(attrs))

	for _, kv := range m.defaultAttributes {
		merged[kv.Key] = kv.Value
	}

	if params != nil {
		for src, target := range m.requestHeaderAttributeMapping {
			if v := lang.CaseInsensitiveValue(params.GetMeta(), src); v != "" {
				merged[attribute.Key(target)] = attribute.StringValue(v)
			}
		}
	}

	for _, kv := range attrs {
		merged[kv.Key] = kv.Value
	}

	all := make([]attribute.KeyValue, 0, len(merged))
	for key, value := range merged {
		all = append(all, attribute.KeyValue{Key: key, Value: value})
	}

	// Sort attributes by key for consistency.
	slices.SortFunc(all, func(a, b attribute.KeyValue) int {
		return strings.Compare(string(a.Key), string(b.Key))
	})

	return metric.WithAttributes(all...)
}
