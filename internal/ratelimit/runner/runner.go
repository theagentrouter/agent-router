// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package runner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"sync"

	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cachetype "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	serverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	rlsconfv3 "github.com/envoyproxy/go-control-plane/ratelimit/config/ratelimit/v3"
	"github.com/go-logr/logr"
	"google.golang.org/grpc"
)

const (
	// NodeID is the xDS node identifier that the rate limit service uses
	// when connecting to this xDS server.
	NodeID = "envoy-ai-gateway-ratelimit"

	// DefaultPort is the default listening port for the rate limit xDS config server.
	DefaultPort = 18002
)

// Runner manages the xDS gRPC server that serves RateLimitConfig resources
// to the rate limit service. It is modeled after Envoy Gateway's
// internal/globalratelimit/runner/runner.go.
type Runner struct {
	logger          logr.Logger
	grpcServer      *grpc.Server
	cache           cachev3.SnapshotCache
	snapshotVersion int64
	port            int
	mu              sync.Mutex
}

// New creates a new rate limit xDS config server runner.
func New(logger logr.Logger, port int) *Runner {
	if port == 0 {
		port = DefaultPort
	}
	return &Runner{
		logger: logger.WithName("ratelimit-xds-runner"),
		port:   port,
	}
}

// Start starts the xDS gRPC server. It blocks until ctx is cancelled.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	r.cache = cachev3.NewSnapshotCache(false, cachev3.IDHash{}, nil)
	r.grpcServer = grpc.NewServer()
	r.mu.Unlock()

	xdsServer := serverv3.NewServer(ctx, r.cache, serverv3.CallbackFuncs{})
	discoveryv3.RegisterAggregatedDiscoveryServiceServer(r.grpcServer, xdsServer)

	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(r.port))
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	go func() {
		<-ctx.Done()
		r.logger.Info("shutting down rate limit xDS config server")
		r.grpcServer.GracefulStop()
	}()

	r.logger.Info("starting rate limit xDS config server", "address", addr)
	// GracefulStop above races with this Serve call, and when it wins Serve returns
	// ErrServerStopped. Only that goroutine stops this server, so it is a clean stop.
	if err := r.grpcServer.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("failed to serve rate limit xDS config: %w", err)
	}
	return nil
}

// UpdateConfigs updates the xDS snapshot cache with the provided rate limit
// configurations. This is called by the QuotaPolicy controller whenever
// a QuotaPolicy is reconciled.
func (r *Runner) UpdateConfigs(ctx context.Context, configs []*rlsconfv3.RateLimitConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cache == nil {
		return fmt.Errorf("snapshot cache not initialized")
	}

	var resources []cachetype.Resource
	for _, cfg := range configs {
		if cfg != nil {
			resources = append(resources, cfg)
		}
	}

	xdsResources := map[resourcev3.Type][]cachetype.Resource{
		resourcev3.RateLimitConfigType: resources,
	}

	// Increment snapshot version.
	if r.snapshotVersion == math.MaxInt64 {
		r.snapshotVersion = 0
	}
	r.snapshotVersion++

	snapshot, err := cachev3.NewSnapshot(fmt.Sprintf("%d", r.snapshotVersion), xdsResources)
	if err != nil {
		return fmt.Errorf("failed to create xDS snapshot: %w", err)
	}

	if err := r.cache.SetSnapshot(ctx, NodeID, snapshot); err != nil {
		return fmt.Errorf("failed to set xDS snapshot: %w", err)
	}

	r.logger.Info("updated rate limit xDS snapshot",
		"version", r.snapshotVersion,
		"numConfigs", len(resources),
	)
	return nil
}
