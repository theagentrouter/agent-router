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
	// MCP Tool Integrity Mismatch is a counter metric that records the total number of tools
	// dropped (or backends entirely emptied) due to a content-digest mismatch, so operators
	// can monitor and alert on unexpected backend tool changes instead of relying on logs
	// alone.
	//
	// Dimensions:
	// - mcp.backend
	// - mcp.tool.name
	// - mcp.tool_integrity.action
	mcpToolIntegrityMismatch = "mcp.tool_integrity.mismatch"
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
	// MCP tool name attribute, used by mcpToolIntegrityMismatch.
	mcpAttributeToolName = "mcp.tool.name"
	// MCP tool integrity action attribute: the configured onMismatch behavior ("Drop" or
	// "DropAll") that fired. Used by mcpToolIntegrityMismatch.
	mcpAttributeToolIntegrityAction = "mcp.tool_integrity.action"
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
	// RecordToolIntegrityMismatch records a tool's content digest failing to match its
	// configured expected digest, so operators can monitor and alert on backend tool drift
	// instead of relying on logs alone. action is the configured onMismatch behavior that
	// fired as a result ("Drop" or "DropAll").
	RecordToolIntegrityMismatch(ctx context.Context, tool string, action string)
}

type mcp struct {
	requestDuration               metric.Float64Histogram
	methodCount                   metric.Float64Counter
	initializationDuration        metric.Float64Histogram
	capabilitiesNegotiated        metric.Float64Counter
	progressNotifications         metric.Float64Counter
	toolIntegrityMismatch         metric.Float64Counter
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
		toolIntegrityMismatch: mustRegisterCounter(
			meter,
			mcpToolIntegrityMismatch,
			metric.WithDescription("Total number of tools dropped due to a content-digest mismatch"),
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
		toolIntegrityMismatch:         m.toolIntegrityMismatch,
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
		toolIntegrityMismatch:         m.toolIntegrityMismatch,
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

// RecordToolIntegrityMismatch implements [MCPMetrics.RecordToolIntegrityMismatch].
func (m *mcp) RecordToolIntegrityMismatch(ctx context.Context, tool string, action string) {
	m.toolIntegrityMismatch.Add(ctx, 1, m.withDefaultAttributes(nil,
		attribute.Key(mcpAttributeToolName).String(tool),
		attribute.Key(mcpAttributeToolIntegrityAction).String(action),
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
