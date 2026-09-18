// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package autoconfig

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	defaultOTLPTCPPort   = 80
	defaultOTLPTLSPort   = 443
	defaultOTLPGPRCPort  = 4317
	logsBackendName      = "otel-logs"
	genericBackendName   = "otel"
	logsProtocolEnv      = "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL"
	genericProtocolEnv   = "OTEL_EXPORTER_OTLP_PROTOCOL"
	logsEndpointEnv      = "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
	genericEndpointEnv   = "OTEL_EXPORTER_OTLP_ENDPOINT"
	logsHeadersEnv       = "OTEL_EXPORTER_OTLP_LOGS_HEADERS"
	genericHeadersEnv    = "OTEL_EXPORTER_OTLP_HEADERS"
	logsExporterEnv      = "OTEL_LOGS_EXPORTER"
	resourceAttrsEnv     = "OTEL_RESOURCE_ATTRIBUTES"
	serviceNameEnv       = "OTEL_SERVICE_NAME"
	exporterValueNone    = "none"
	exporterValueConsole = "console"
)

type otelHeader struct {
	Name  string
	Value string
}

type otelResourceAttr struct {
	Key   string
	Value string
}

type otelLogConfig struct {
	Exporter    string
	BackendName string
	Headers     []otelHeader
	Resources   []otelResourceAttr
}

// PopulateOTELLogEnvConfig configures OTLP access logging based on OTEL env vars.
// Only gRPC transport is supported, following OTEL precedence rules:
//   - OTEL_EXPORTER_OTLP_LOGS_ENDPOINT > OTEL_EXPORTER_OTLP_ENDPOINT
//   - OTEL_EXPORTER_OTLP_LOGS_HEADERS > OTEL_EXPORTER_OTLP_HEADERS
//   - OTEL_SERVICE_NAME overrides service.name in OTEL_RESOURCE_ATTRIBUTES
//
// Behavior:
//   - OTEL_LOGS_EXPORTER=none disables access logs (no accessLog section).
//   - OTEL_LOGS_EXPORTER=console uses the file sink (/dev/stdout).
//   - OTEL_LOGS_EXPORTER=otlp uses OTLP sink when gRPC + endpoint are present.
//   - OTEL_LOGS_EXPORTER unset defaults to OTLP if an endpoint is configured, otherwise console.
func PopulateOTELLogEnvConfig(data *ConfigData) error {
	if data == nil {
		return fmt.Errorf("ConfigData cannot be nil")
	}

	endpoint, backendName := otelEndpoint()
	exporter := strings.ToLower(strings.TrimSpace(os.Getenv(logsExporterEnv)))
	switch exporter {
	case exporterValueNone:
		data.OTELLog = &otelLogConfig{Exporter: exporterValueNone}
		return nil
	case exporterValueConsole:
		data.OTELLog = &otelLogConfig{Exporter: exporter}
		return nil
	case "otlp":
		// explicitly opted in
	case "":
		if endpoint == "" {
			data.OTELLog = &otelLogConfig{Exporter: exporterValueConsole}
			return nil
		}
	default:
		return fmt.Errorf("unsupported OTEL_LOGS_EXPORTER value %q (allowed: otlp, console, none)", exporter)
	}

	proto := firstNonEmpty(
		strings.ToLower(strings.TrimSpace(os.Getenv(logsProtocolEnv))),
		strings.ToLower(strings.TrimSpace(os.Getenv(genericProtocolEnv))),
	)
	if proto == "" {
		proto = "grpc"
	}
	if proto != "grpc" {
		return fmt.Errorf("OTEL logs support gRPC protocol only, got %q", proto)
	}

	host, port, needsTLS, err := parseOTELEndpoint(endpoint)
	if err != nil {
		return err
	}

	headers := parseOTELHeaders(firstNonEmpty(os.Getenv(logsHeadersEnv), os.Getenv(genericHeadersEnv)))
	resources := parseOTELResourceAttributes(os.Getenv(resourceAttrsEnv), os.Getenv(serviceNameEnv))

	hostname, ip := splitHost(host)
	if err := upsertBackend(data, Backend{
		Name:        backendName,
		Hostname:    hostname,
		IP:          ip,
		Port:        port,
		NeedsTLS:    needsTLS,
		IsTelemetry: true,
	}); err != nil {
		return err
	}
	data.OTELLog = &otelLogConfig{
		Exporter:    "otlp",
		BackendName: backendName,
		Headers:     headers,
		Resources:   resources,
	}
	return nil
}

func otelEndpoint() (endpoint string, backendName string) {
	logsEndpoint := strings.TrimSpace(os.Getenv(logsEndpointEnv))
	genericEndpoint := strings.TrimSpace(os.Getenv(genericEndpointEnv))
	if logsEndpoint != "" {
		return logsEndpoint, logsBackendName
	}
	if genericEndpoint != "" {
		return genericEndpoint, genericBackendName
	}
	return "", ""
}

func parseOTELEndpoint(raw string) (hostname string, port int, needsTLS bool, err error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", 0, false, fmt.Errorf("invalid OTLP endpoint: empty")
	}

	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid OTLP endpoint %q: %w", raw, err)
	}

	hostname = u.Hostname()
	if hostname == "" {
		return "", 0, false, fmt.Errorf("invalid OTLP endpoint %q: missing host", raw)
	}

	needsTLS = strings.EqualFold(u.Scheme, "https")

	switch {
	case u.Port() != "":
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return "", 0, false, fmt.Errorf("invalid OTLP endpoint %q: %w", raw, err)
		}
	case needsTLS:
		port = defaultOTLPTLSPort
	default:
		port = defaultOTLPTCPPort
	}

	return hostname, port, needsTLS, nil
}

func parseOTELHeaders(value string) []otelHeader {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	headers := make([]otelHeader, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		val = strings.TrimSpace(val)
		if name == "" {
			continue
		}
		headers = append(headers, otelHeader{Name: name, Value: val})
	}
	return headers
}

func parseOTELResourceAttributes(value string, serviceName string) []otelResourceAttr {
	raw := strings.TrimSpace(value)
	resources := make(map[string]string)

	if raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			key, val, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			if key == "" {
				continue
			}
			if decoded, err := url.PathUnescape(val); err == nil && decoded != "" {
				val = decoded
			}
			resources[key] = val
		}
	}

	if svc := strings.TrimSpace(serviceName); svc != "" {
		resources["service.name"] = svc
	}

	if len(resources) == 0 {
		return nil
	}

	keys := make([]string, 0, len(resources))
	for k := range resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	result := make([]otelResourceAttr, 0, len(keys))
	for _, k := range keys {
		result = append(result, otelResourceAttr{Key: k, Value: resources[k]})
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func upsertBackend(data *ConfigData, backend Backend) error {
	for i, existing := range data.Backends {
		if existing.Name != backend.Name {
			continue
		}
		if existing != backend {
			return fmt.Errorf("backend %q already configured with different settings", backend.Name)
		}
		data.Backends[i] = backend
		return nil
	}
	data.Backends = append(data.Backends, backend)
	return nil
}
