// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extensionserver

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	egextension "github.com/envoyproxy/gateway/proto/extension"
	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/envoyproxy/ai-gateway/internal/requestheaderattrs"
)

// Server is the implementation of the EnvoyGatewayExtensionServer interface.
type Server struct {
	egextension.UnimplementedEnvoyGatewayExtensionServer
	log       logr.Logger
	k8sClient client.Client
	// udsPath is the path to the UDS socket.
	// This is used to communicate with the external processor.
	udsPath          string
	isStandAloneMode bool
	// logRequestHeaderAttributes maps request headers to dynamic metadata keys for access logs.
	logRequestHeaderAttributes map[string]string
	// quotaRateLimitServiceHost is the hostname for the AI Gateway quota rate limit service.
	quotaRateLimitServiceHost string
	// quotaRateLimitServicePort is the gRPC port for the AI Gateway quota rate limit service.
	quotaRateLimitServicePort uint32
	// quotaRateLimitTimeout is the timeout for the rate limit service.
	quotaRateLimitTimeout int64
	// quotaRateLimitFailureModeDeny sets the failure mode for the rate limit filter.
	quotaRateLimitFailureModeDeny bool
}

const serverName = "envoy-gateway-extension-server"

// New creates a new instance of the extension server that implements the EnvoyGatewayExtensionServer interface.
func New(k8sClient client.Client, logger logr.Logger, udsPath string, isStandAloneMode bool, requestHeaderAttributes, logRequestHeaderAttributes *string, quotaRateLimitServiceAddr string, quotaRateLimitTimeout int64, quotaRateLimitFailureModeDeny bool) (*Server, error) {
	logger = logger.WithName(serverName)
	logAttrs, err := requestheaderattrs.ResolveLog(requestHeaderAttributes, logRequestHeaderAttributes)
	if err != nil {
		return nil, err
	}

	host, port, err := parseHostPort(quotaRateLimitServiceAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid quotaRateLimitServiceAddr %q: %w", quotaRateLimitServiceAddr, err)
	}

	return &Server{
		log:                           logger,
		k8sClient:                     k8sClient,
		udsPath:                       udsPath,
		isStandAloneMode:              isStandAloneMode,
		logRequestHeaderAttributes:    logAttrs,
		quotaRateLimitServiceHost:     host,
		quotaRateLimitServicePort:     port,
		quotaRateLimitTimeout:         quotaRateLimitTimeout,
		quotaRateLimitFailureModeDeny: quotaRateLimitFailureModeDeny,
	}, nil
}

// parseHostPort splits a "host:port" string. If no port is present,
// defaultQuotaRateLimitServicePort is used.
func parseHostPort(hostPort string) (string, uint32, error) {
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		// A hostname without a port and a bare IPv6 literal both use the default port.
		if hostPort != "" && (!strings.Contains(hostPort, ":") || net.ParseIP(hostPort) != nil) {
			return hostPort, defaultQuotaRateLimitServicePort, nil
		}
		return "", 0, fmt.Errorf("invalid host:port: %w", err)
	}
	if host == "" {
		return "", 0, fmt.Errorf("host must be non-empty")
	}
	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %q: %w", portStr, err)
	}
	if port == 0 {
		return "", 0, fmt.Errorf("port must be non-zero")
	}
	return host, uint32(port), nil
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
		serverName: {Status: grpc_health_v1.HealthCheckResponse_SERVING},
	}}, nil
}

// toAny marshals the provided message to an Any message.
func toAny(msg proto.Message) (*anypb.Any, error) {
	b, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message to Any: %w", err)
	}
	const envoyAPIPrefix = "type.googleapis.com/"
	return &anypb.Any{
		TypeUrl: envoyAPIPrefix + string(msg.ProtoReflect().Descriptor().FullName()),
		Value:   b,
	}, nil
}
