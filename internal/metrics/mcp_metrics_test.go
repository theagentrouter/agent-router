// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package metrics

import (
	"net/http"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/envoyproxy/ai-gateway/internal/testing/testotel"
)

func TestNewMCP(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)
}

func TestRecordMetricWithCustomAttributes(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, map[string]string{
		"x-tracing-enrichment-user-region": "user.region",
		"agent-session-id":                 "session.id",
		"CustomAttr":                       "custom.attr",
	})
	require.NotNil(t, m)

	req, err := http.NewRequest("GET", "https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("X-Tracing-Enrichment-User-Region", "us-east-1") // should be included in metrics
	req.Header.Set("X-Other-Attr", "other")                         // should be ignored
	req.Header.Set("agent-session-id", "123")                       // should be ignored as the value in the metadata takes precedence

	m = m.WithRequestAttributes(req)

	startAt := time.Now().Add(-1 * time.Minute)
	m.RecordRequestDuration(t.Context(), startAt, &mcpsdk.InitializeParams{
		Meta: map[string]any{
			"Agent-Session-Id": "sess-4567", // alphabetical order wins when multiple values match case-insensitively
			"agent-session-id": "sess-1234",
			"customattr":       "custom-value1", // exact match should win over case-insensitive match
			"CustomAttr":       "custom-value2",
		},
	})

	count, sum := testotel.GetHistogramValues(t, mr, mcpRequestDuration,
		attribute.NewSet(
			attribute.String("user.region", "us-east-1"),
			attribute.String("session.id", "sess-4567"),
			attribute.String("custom.attr", "custom-value2"),
		))
	require.Equal(t, uint64(1), count)
	require.Equal(t, 60, int(sum))
}

func TestRecordRequestDuration(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)
	startAt := time.Now().Add(-1 * time.Minute)
	m.RecordRequestDuration(t.Context(), startAt, nil)

	count, sum := testotel.GetHistogramValues(t, mr, mcpRequestDuration, attribute.NewSet())
	require.Equal(t, uint64(1), count)
	require.Equal(t, 60, int(sum))
}

func TestRecordRequestErrorDuration(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)
	startAt := time.Now().Add(-30 * time.Second)
	m.RecordRequestErrorDuration(t.Context(), startAt, MCPErrorUnsupportedProtocolVersion, nil)

	count, sum := testotel.GetHistogramValues(t, mr, mcpRequestDuration, attribute.NewSet(
		attribute.Key(mcpAttributeErrorType).String(string(MCPErrorUnsupportedProtocolVersion)),
	))
	require.Equal(t, uint64(1), count)
	require.Equal(t, 30, int(sum))
}

func TestRecordMethodCount(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)

	m.RecordMethodCount(t.Context(), "test_method_name", nil)
	attrs := attribute.NewSet(
		attribute.Key(mcpAttributeMethodName).String("test_method_name"),
		attribute.Key(mcpAttributeStatusName).String(string(MCPStatusSuccess)),
	)
	val := testotel.GetCounterValue(t, mr, mcpMethodCount, attrs)
	require.Equal(t, float64(1), val)

	m.RecordMethodErrorCount(t.Context(), "test_method_name", nil, MCPStatusError)
	attrs = attribute.NewSet(
		attribute.Key(mcpAttributeMethodName).String("test_method_name"),
		attribute.Key(mcpAttributeStatusName).String(string(MCPStatusError)),
	)
	val = testotel.GetCounterValue(t, mr, mcpMethodCount, attrs)
	require.Equal(t, float64(1), val)
}

func TestRecordInitializationDuration(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)

	startAt := time.Now().Add(-45 * time.Second)
	m.RecordInitializationDuration(t.Context(), startAt, nil)

	count, sum := testotel.GetHistogramValues(t, mr, mcpInitializationDuration, attribute.NewSet())
	require.Equal(t, uint64(1), count)
	require.Equal(t, 45, int(sum))
}

func TestRecordCapabilitiesNegotiated(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)

	m.RecordClientCapabilities(t.Context(), &mcpsdk.ClientCapabilities{
		Experimental: map[string]any{
			"exp1": struct{}{},
			"exp2": struct{}{},
		},
		Roots: struct {
			ListChanged bool "json:\"listChanged,omitempty\""
		}{ListChanged: true},
		Sampling: &mcpsdk.SamplingCapabilities{},
	}, nil)
	m.RecordServerCapabilities(t.Context(), &mcpsdk.ServerCapabilities{
		Experimental: map[string]any{
			"exp1": struct{}{},
		},
		Completions: &mcpsdk.CompletionCapabilities{},
		Logging:     &mcpsdk.LoggingCapabilities{},
		Prompts:     &mcpsdk.PromptCapabilities{ListChanged: true},
		Resources:   &mcpsdk.ResourceCapabilities{ListChanged: true, Subscribe: true},
		Tools:       &mcpsdk.ToolCapabilities{ListChanged: true},
	}, nil)
	require.Equal(t, float64(2), testotel.GetCounterValue(t, mr, mcpCapabilitiesNegotiated, attribute.NewSet(
		attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeExperimental)),
		attribute.Key(mcpAttributeCapabilitySide).String(string(mcpCapabilitySideClient)),
	)))
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpCapabilitiesNegotiated, attribute.NewSet(
		attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeRoots)),
		attribute.Key(mcpAttributeCapabilitySide).String(string(mcpCapabilitySideClient)),
	)))

	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpCapabilitiesNegotiated, attribute.NewSet(
		attribute.Key(mcpAttributeCapabilityType).String(string(mcpCapabilityTypeSampling)),
		attribute.Key(mcpAttributeCapabilitySide).String(string(mcpCapabilitySideClient)),
	)))

	for _, serverCapability := range []mcpCapabilityType{
		mcpCapabilityTypeExperimental,
		mcpCapabilityTypeCompletions,
		mcpCapabilityTypePrompts,
		mcpCapabilityTypeResources,
		mcpCapabilityTypeTools,
	} {
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpCapabilitiesNegotiated, attribute.NewSet(
			attribute.Key(mcpAttributeCapabilityType).String(string(serverCapability)),
			attribute.Key(mcpAttributeCapabilitySide).String(string(mcpCapabilitySideServer)),
		)))
	}
}

func TestRecordProgressNotifications(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)

	m.RecordProgress(t.Context(), nil)
	val := testotel.GetCounterValue(t, mr, mpcProgressNotifications, attribute.NewSet())
	require.Equal(t, float64(1), val)

	m.RecordProgress(t.Context(), nil)
	val = testotel.GetCounterValue(t, mr, mpcProgressNotifications, attribute.NewSet())
	require.Equal(t, float64(2), val)
}

func TestRecordNotificationStreamLifecycle(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil).WithBackend("backend1")
	backendAttrs := attribute.NewSet(attribute.String(mcpAttributeBackend, "backend1"))

	m.RecordNotificationStreamOpenAttempt(t.Context())
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpNotificationStreamOpenAttempts, backendAttrs))

	m.RecordNotificationStreamOpenOutcome(t.Context(), MCPNotificationStreamOutcomeOpened)
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpNotificationStreamOpenOutcomes, attribute.NewSet(
		attribute.String(mcpAttributeBackend, "backend1"),
		attribute.String(mcpAttributeStreamOutcome, string(MCPNotificationStreamOutcomeOpened)),
	)))
	require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpNotificationStreamActive, backendAttrs))

	m.RecordNotificationStreamEnd(t.Context(), time.Now().Add(-30*time.Second), MCPNotificationStreamEndReasonEOF)
	require.Equal(t, float64(0), testotel.GetCounterValue(t, mr, mcpNotificationStreamActive, backendAttrs))
	count, sum := testotel.GetHistogramValues(t, mr, mcpNotificationStreamDuration, attribute.NewSet(
		attribute.String(mcpAttributeBackend, "backend1"),
		attribute.String(mcpAttributeStreamEndReason, string(MCPNotificationStreamEndReasonEOF)),
	))
	require.Equal(t, uint64(1), count)
	// The lower bound is exact; the upper bound only tolerates scheduler pauses.
	require.GreaterOrEqual(t, sum, 30.0)
	require.Less(t, sum, 40.0)

	// The lifetime histogram is in seconds with buckets that cover short failures and long-lived streams.
	var data metricdata.ResourceMetrics
	require.NoError(t, mr.Collect(t.Context(), &data))
	var found bool
	for _, sm := range data.ScopeMetrics {
		for _, md := range sm.Metrics {
			if md.Name != mcpNotificationStreamDuration {
				continue
			}
			found = true
			require.Equal(t, "s", md.Unit)
			dps := md.Data.(metricdata.Histogram[float64]).DataPoints
			require.Len(t, dps, 1)
			require.Equal(t, []float64{0.1, 1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200}, dps[0].Bounds)
		}
	}
	require.True(t, found)
}

func TestRecordNotificationStreamOpenOutcome(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil).WithBackend("backend1")
	backendAttrs := attribute.NewSet(attribute.String(mcpAttributeBackend, "backend1"))
	// One open stream so that the active gauge exists and can be checked for changes.
	m.RecordNotificationStreamOpenOutcome(t.Context(), MCPNotificationStreamOutcomeOpened)

	for _, outcome := range []MCPNotificationStreamOutcome{
		MCPNotificationStreamOutcomeUnsupported,
		MCPNotificationStreamOutcomeHTTP4xx,
		MCPNotificationStreamOutcomeHTTP5xx,
		MCPNotificationStreamOutcomeHTTPOther,
		MCPNotificationStreamOutcomeTransportError,
		MCPNotificationStreamOutcomeCancelled,
	} {
		m.RecordNotificationStreamOpenOutcome(t.Context(), outcome)
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpNotificationStreamOpenOutcomes, attribute.NewSet(
			attribute.String(mcpAttributeBackend, "backend1"),
			attribute.String(mcpAttributeStreamOutcome, string(outcome)),
		)), "outcome %s", outcome)
		// Only the opened outcome changes the number of active streams.
		require.Equal(t, float64(1), testotel.GetCounterValue(t, mr, mcpNotificationStreamActive, backendAttrs), "outcome %s", outcome)
	}
}

func TestRecordNotificationStreamEnd(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil).WithBackend("backend1")
	backendAttrs := attribute.NewSet(attribute.String(mcpAttributeBackend, "backend1"))

	reasons := []MCPNotificationStreamEndReason{
		MCPNotificationStreamEndReasonEOF,
		MCPNotificationStreamEndReasonCancelled,
		MCPNotificationStreamEndReasonError,
	}
	for range reasons {
		m.RecordNotificationStreamOpenOutcome(t.Context(), MCPNotificationStreamOutcomeOpened)
	}
	require.Equal(t, float64(len(reasons)), testotel.GetCounterValue(t, mr, mcpNotificationStreamActive, backendAttrs))

	for i, reason := range reasons {
		m.RecordNotificationStreamEnd(t.Context(), time.Now().Add(-10*time.Second), reason)
		count, sum := testotel.GetHistogramValues(t, mr, mcpNotificationStreamDuration, attribute.NewSet(
			attribute.String(mcpAttributeBackend, "backend1"),
			attribute.String(mcpAttributeStreamEndReason, string(reason)),
		))
		require.Equal(t, uint64(1), count, "reason %s", reason)
		require.GreaterOrEqual(t, sum, 10.0, "reason %s", reason)
		require.Less(t, sum, 20.0, "reason %s", reason)
		require.Equal(t, float64(len(reasons)-i-1), testotel.GetCounterValue(t, mr, mcpNotificationStreamActive, backendAttrs))
	}
}

func TestWithBackend(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, nil)
	require.NotNil(t, m)

	// Record metrics with backend label
	mWithBackend := m.WithBackend("test-backend")
	startAt := time.Now().Add(-1 * time.Minute)
	mWithBackend.RecordRequestDuration(t.Context(), startAt, nil)

	count, sum := testotel.GetHistogramValues(t, mr, mcpRequestDuration,
		attribute.NewSet(
			attribute.String(mcpAttributeBackend, "test-backend"),
		))
	require.Equal(t, uint64(1), count)
	require.Equal(t, 60, int(sum))

	// Test method count with backend
	mWithBackend.RecordMethodCount(t.Context(), "tools/call", nil)
	attrs := attribute.NewSet(
		attribute.String(mcpAttributeBackend, "test-backend"),
		attribute.Key(mcpAttributeMethodName).String("tools/call"),
		attribute.Key(mcpAttributeStatusName).String(string(MCPStatusSuccess)),
	)
	val := testotel.GetCounterValue(t, mr, mcpMethodCount, attrs)
	require.Equal(t, float64(1), val)

	// Test initialization duration with backend
	mWithBackend2 := m.WithBackend("another-backend")
	initStart := time.Now().Add(-30 * time.Second)
	mWithBackend2.RecordInitializationDuration(t.Context(), initStart, nil)

	count, sum = testotel.GetHistogramValues(t, mr, mcpInitializationDuration,
		attribute.NewSet(
			attribute.String(mcpAttributeBackend, "another-backend"),
		))
	require.Equal(t, uint64(1), count)
	require.Equal(t, 30, int(sum))
}

func TestWithBackendAndRequestAttributes(t *testing.T) {
	mr := metric.NewManualReader()
	meter := metric.NewMeterProvider(metric.WithReader(mr)).Meter("test")

	m := NewMCP(meter, map[string]string{
		"x-region": "user.region",
	})
	require.NotNil(t, m)

	req, err := http.NewRequest("GET", "https://example.com", nil)
	require.NoError(t, err)
	req.Header.Set("X-Region", "us-west-2")

	// Chain WithRequestAttributes and WithBackend
	mWithAttrs := m.WithRequestAttributes(req).WithBackend("backend-1")

	startAt := time.Now().Add(-20 * time.Second)
	mWithAttrs.RecordRequestDuration(t.Context(), startAt, nil)

	count, sum := testotel.GetHistogramValues(t, mr, mcpRequestDuration,
		attribute.NewSet(
			attribute.String("user.region", "us-west-2"),
			attribute.String(mcpAttributeBackend, "backend-1"),
		))
	require.Equal(t, uint64(1), count)
	require.Equal(t, 20, int(sum))
}
