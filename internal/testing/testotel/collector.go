// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package testotel provides test utilities for OpenTelemetry tracing, metrics, and logs tests.
// This is not internal for use in cmd/extproc/mainlib/main_test.go.
package testotel

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	collectlogsv1 "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttracev1 "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

// otlpTimeout is the timeout for spans to read back.
const otlpTimeout = 1 * time.Second // OTEL_BSP_SCHEDULE_DELAY + overhead..

// traceServer implements the OTLP trace service.
type traceServer struct {
	collecttracev1.UnimplementedTraceServiceServer
	spanCh chan *tracev1.ResourceSpans
}

func (s *traceServer) Export(_ context.Context, req *collecttracev1.ExportTraceServiceRequest) (*collecttracev1.ExportTraceServiceResponse, error) {
	for _, resourceSpans := range req.ResourceSpans {
		timeout := time.After(otlpTimeout)
		select {
		case s.spanCh <- resourceSpans:
		case <-timeout:
			// Avoid blocking if the channel is full. Likely indicates a test issue or spans not being read like
			// the ones emitted during test shutdown. Otherwise, server shutdown blocks the test indefinitely.
			fmt.Println("Warning: Dropping spans due to timeout")
		}
	}
	return &collecttracev1.ExportTraceServiceResponse{}, nil
}

// metricsServer implements the OTLP metrics service.
type metricsServer struct {
	collectmetricsv1.UnimplementedMetricsServiceServer
	metricsCh chan *metricsv1.ResourceMetrics
}

func (s *metricsServer) Export(_ context.Context, req *collectmetricsv1.ExportMetricsServiceRequest) (*collectmetricsv1.ExportMetricsServiceResponse, error) {
	for _, resourceMetrics := range req.ResourceMetrics {
		timeout := time.After(otlpTimeout)
		select {
		case s.metricsCh <- resourceMetrics:
		case <-timeout:
			// Avoid blocking if the channel is full. Likely indicates a test issue or metrics not being read like
			// the ones emitted during test shutdown. Otherwise, server shutdown blocks the test indefinitely.
			fmt.Println("Warning: Dropping metrics due to timeout")
		}
	}
	return &collectmetricsv1.ExportMetricsServiceResponse{}, nil
}

// logsServer implements the OTLP logs service.
type logsServer struct {
	collectlogsv1.UnimplementedLogsServiceServer
	logsCh chan *logsv1.ResourceLogs
}

func (s *logsServer) Export(_ context.Context, req *collectlogsv1.ExportLogsServiceRequest) (*collectlogsv1.ExportLogsServiceResponse, error) {
	for _, resourceLogs := range req.ResourceLogs {
		timeout := time.After(otlpTimeout)
		select {
		case s.logsCh <- resourceLogs:
		case <-timeout:
			fmt.Println("Warning: Dropping logs due to timeout")
		}
	}
	return &collectlogsv1.ExportLogsServiceResponse{}, nil
}

// StartOTLPCollector starts a test OTLP collector server that receives trace, metrics, and logs data via gRPC.
func StartOTLPCollector() *OTLPCollector {
	spanCh := make(chan *tracev1.ResourceSpans, 10)
	metricsCh := make(chan *metricsv1.ResourceMetrics, 10)
	logsCh := make(chan *logsv1.ResourceLogs, 10)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("failed to listen: %v", err))
	}

	server := grpc.NewServer()
	collecttracev1.RegisterTraceServiceServer(server, &traceServer{spanCh: spanCh})
	collectmetricsv1.RegisterMetricsServiceServer(server, &metricsServer{metricsCh: metricsCh})
	collectlogsv1.RegisterLogsServiceServer(server, &logsServer{logsCh: logsCh})

	go func() {
		// Server.Serve returns error on Stop/GracefulStop which is expected.
		_ = server.Serve(listener)
	}()

	endpoint := fmt.Sprintf("http://%s", listener.Addr().String())
	env := []string{
		fmt.Sprintf("OTEL_EXPORTER_OTLP_ENDPOINT=%s", endpoint),
		"OTEL_EXPORTER_OTLP_PROTOCOL=grpc",
		"OTEL_SERVICE_NAME=ai-gateway-extproc",
		"OTEL_BSP_SCHEDULE_DELAY=100",
		"OTEL_METRIC_EXPORT_INTERVAL=100",
		// Use delta temporality to prevent metric accumulation across subtests.
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta",
	}
	return &OTLPCollector{
		server:    server,
		listener:  listener,
		env:       env,
		spanCh:    spanCh,
		metricsCh: metricsCh,
		logsCh:    logsCh,
	}
}

type OTLPCollector struct {
	server    *grpc.Server
	listener  net.Listener
	env       []string
	spanCh    chan *tracev1.ResourceSpans
	metricsCh chan *metricsv1.ResourceMetrics
	logsCh    chan *logsv1.ResourceLogs
	// pendingSpans holds spans from a multi-span OTLP batch that TakeSpan has
	// not yet returned. The batch processor often exports server/discover and
	// Initialize together; returning only Spans[0] would drop the rest.
	pendingSpans []*tracev1.Span
}

// Env returns the environment variables needed to configure the OTLP collector.
func (o *OTLPCollector) Env() []string {
	return o.env
}

// SetEnv calls setenv for each environment variable in Env.
func (o *OTLPCollector) SetEnv(setenv func(key string, value string)) {
	for _, env := range o.Env() {
		kv := strings.SplitN(env, "=", 2)
		if len(kv) == 2 {
			setenv(kv[0], kv[1])
		}
	}
}

// TakeSpan returns a single span or nil if none were recorded.
// When an OTLP export contains multiple spans, remaining spans are queued and
// returned by subsequent TakeSpan calls so batched exports are not dropped.
func (o *OTLPCollector) TakeSpan() *tracev1.Span {
	if len(o.pendingSpans) > 0 {
		span := o.pendingSpans[0]
		o.pendingSpans = o.pendingSpans[1:]
		return span
	}
	select {
	case resourceSpans := <-o.spanCh:
		var spans []*tracev1.Span
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			spans = append(spans, scopeSpans.Spans...)
		}
		if len(spans) == 0 {
			return nil
		}
		if len(spans) > 1 {
			o.pendingSpans = spans[1:]
		}
		return spans[0]
	case <-time.After(otlpTimeout):
		return nil
	}
}

// DrainMetrics returns metrics or nil if none were recorded.
func (o *OTLPCollector) DrainMetrics() *metricsv1.ResourceMetrics {
	select {
	case resourceMetrics := <-o.metricsCh:
		return resourceMetrics
	case <-time.After(otlpTimeout):
		return nil
	}
}

// TakeMetrics collects metrics until the expected count is reached or a timeout occurs.
func (o *OTLPCollector) TakeMetrics(expectedCount int) []*metricsv1.ResourceMetrics {
	var metrics []*metricsv1.ResourceMetrics
	deadline := time.After(otlpTimeout)

	// Helper to count total metrics across all ResourceMetrics.
	countMetrics := func() int {
		total := 0
		for _, rm := range metrics {
			for _, sm := range rm.ScopeMetrics {
				total += len(sm.Metrics)
			}
		}
		return total
	}

	for {
		select {
		case resourceMetrics := <-o.metricsCh:
			metrics = append(metrics, resourceMetrics)
			if countMetrics() >= expectedCount {
				// Drain any additional metrics that arrive immediately after.
				time.Sleep(50 * time.Millisecond)
			drainLoop:
				for {
					select {
					case rm := <-o.metricsCh:
						metrics = append(metrics, rm)
					default:
						break drainLoop
					}
				}
				return metrics
			}
		case <-deadline:
			return metrics
		}
	}
}

// TakeLog returns a single log entry or nil if none were recorded.
func (o *OTLPCollector) TakeLog() *logsv1.ResourceLogs {
	select {
	case resourceLogs := <-o.logsCh:
		return resourceLogs
	case <-time.After(otlpTimeout):
		return nil
	}
}

// Close shuts down the collector and cleans up resources.
func (o *OTLPCollector) Close() {
	o.server.GracefulStop()
	o.listener.Close()
}
