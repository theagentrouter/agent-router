// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

// The tests in this file change the configuration under a running server, as a route change on a live gateway does:
// Envoy gets the new route from Envoy Gateway, while this filter reads the new filter config from the mounted bundle
// on its own schedule. For a few seconds the two can disagree on the backend names.

const configChangeTestPath = "/v1/chat/completions"

var (
	configChangeTestBackend0 = internalapi.PerRouteRuleRefBackendName("default", "chat", "chat", 0, 0)
	configChangeTestBackend1 = internalapi.PerRouteRuleRefBackendName("default", "chat", "chat", 0, 1)
)

// configChangeTestProcessor is the upstream filter's processor. It records the backend it is given.
type configChangeTestProcessor struct {
	passThroughProcessor
	backend string
}

// SetBackend implements [Processor.SetBackend].
func (p *configChangeTestProcessor) SetBackend(_ context.Context, b *filterapi.RuntimeBackend, _ string, _ Processor) error {
	p.backend = b.Backend.Name
	return nil
}

// configChangeTestStream sends the server the given requests, then ends the stream, and records the responses.
type configChangeTestStream struct {
	grpc.ServerStream
	ctx  context.Context
	reqs []*extprocv3.ProcessingRequest
	sent []*extprocv3.ProcessingResponse
}

// Context implements [extprocv3.ExternalProcessor_ProcessServer].
func (s *configChangeTestStream) Context() context.Context { return s.ctx }

// Recv implements [extprocv3.ExternalProcessor_ProcessServer].
func (s *configChangeTestStream) Recv() (*extprocv3.ProcessingRequest, error) {
	if len(s.reqs) == 0 {
		return nil, io.EOF
	}
	req := s.reqs[0]
	s.reqs = s.reqs[1:]
	return req, nil
}

// Send implements [extprocv3.ExternalProcessor_ProcessServer].
func (s *configChangeTestStream) Send(resp *extprocv3.ProcessingResponse) error {
	s.sent = append(s.sent, resp)
	return nil
}

// writeConfigChangeTestBundle writes a one-part filter config bundle with the given backends into dir, and sets the
// modification time of its files to mtime.
func writeConfigChangeTestBundle(t *testing.T, dir string, mtime time.Time, backends ...string) {
	t.Helper()
	var cfg strings.Builder
	cfg.WriteString("version: dev\nbackends:\n")
	for _, b := range backends {
		cfg.WriteString("- name: " + b + "\n")
	}
	raw := []byte(cfg.String())
	part := filepath.Join(dir, "parts", "000")
	require.NoError(t, os.MkdirAll(filepath.Dir(part), 0o700))
	require.NoError(t, os.WriteFile(part, raw, 0o600))
	idx, err := filterapi.MarshalConfigBundleIndex(&filterapi.ConfigBundleIndex{
		Checksum: filterapi.ConfigBundleChecksum(raw),
		Parts:    []filterapi.ConfigBundlePart{{Name: "part-000", Path: "parts/000"}},
	})
	require.NoError(t, err)
	index := filepath.Join(dir, filterapi.ConfigBundleIndexFileName)
	require.NoError(t, os.WriteFile(index, idx, 0o600))
	for _, f := range []string{part, index} {
		require.NoError(t, os.Chtimes(f, mtime, mtime))
	}
}

// newConfigChangeTestServer starts a server that loads its config from a bundle in dir with the ext-proc's own
// watcher, and registers a router filter request whose upstream filter request the test then sends. The watcher
// ticks once an hour, so a bundle the test writes later is not loaded by a tick.
func newConfigChangeTestServer(t *testing.T, dir string, backends ...string) (*Server, *configChangeTestProcessor) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := NewServer(logger, false)
	require.NoError(t, err)
	upstream := &configChangeTestProcessor{}
	s.Register(configChangeTestPath, func(_ *filterapi.RuntimeConfig, _ map[string]string, _ *slog.Logger, isUpstreamFilter bool, _ bool) (Processor, error) {
		if isUpstreamFilter {
			return upstream, nil
		}
		return passThroughProcessor{}, nil
	})
	writeConfigChangeTestBundle(t, dir, time.Now(), backends...)
	require.NoError(t, filterapi.StartConfigBundleWatcher(t.Context(), dir, s, logger, time.Hour))
	s.routerProcessorsPerReqID["req-1"] = passThroughProcessor{}
	return s, upstream
}

// configChangeTestUpstreamRequest is the request headers message from the upstream ext_proc filter after Envoy
// picked the given backend.
func configChangeTestUpstreamRequest(backend string) *extprocv3.ProcessingRequest {
	return &extprocv3.ProcessingRequest{
		Attributes: map[string]*structpb.Struct{"envoy.filters.http.ext_proc": {Fields: map[string]*structpb.Value{
			internalapi.XDSUpstreamHostMetadataBackendNamePath: structpb.NewStringValue(backend),
		}}},
		Request: &extprocv3.ProcessingRequest_RequestHeaders{RequestHeaders: &extprocv3.HttpHeaders{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
				{Key: originalPathHeader, Value: configChangeTestPath},
				{Key: internalReqIDHeader, Value: "req-1"},
			}},
		}},
	}
}

// Envoy already routes to a backend that was just added to the route. The controller has written the bundle that
// has it, but the watcher has not loaded that bundle yet.
func TestServer_requestForBackendAddedBeforeConfigLoad(t *testing.T) {
	dir := t.TempDir()
	s, upstream := newConfigChangeTestServer(t, dir, configChangeTestBackend0)
	writeConfigChangeTestBundle(t, dir, time.Now().Add(time.Minute), configChangeTestBackend0, configChangeTestBackend1)

	stream := &configChangeTestStream{ctx: t.Context(), reqs: []*extprocv3.ProcessingRequest{
		configChangeTestUpstreamRequest(configChangeTestBackend1),
	}}
	err := s.Process(stream)
	require.NoError(t, err, "the stream ended with an error, which Envoy turns into HTTP 500 for the client")
	require.Equal(t, configChangeTestBackend1, upstream.backend)
	require.Len(t, stream.sent, 1)
}

// The loaded config does not have the backend Envoy routes to, and the bundle on disk does not either: the
// filter has loaded a change that Envoy has not applied yet, or the reverse with the bundle not yet on disk.
func TestServer_requestForBackendNotInConfig(t *testing.T) {
	dir := t.TempDir()
	s, upstream := newConfigChangeTestServer(t, dir, configChangeTestBackend0)

	stream := &configChangeTestStream{ctx: t.Context(), reqs: []*extprocv3.ProcessingRequest{
		configChangeTestUpstreamRequest(configChangeTestBackend1),
	}}
	err := s.Process(stream)
	require.Equal(t, codes.Unavailable, status.Code(err), "stream error: %v", err)
	require.Len(t, stream.sent, 1, "without an immediate response, Envoy answers the client with HTTP 500")
	ir := stream.sent[0].GetImmediateResponse()
	require.NotNil(t, ir, "sent: %v", stream.sent[0])
	require.Equal(t, typev3.StatusCode_ServiceUnavailable, ir.GetStatus().GetCode())
	require.Equal(t, uint32(codes.Unavailable), ir.GetGrpcStatus().GetStatus())
	require.Empty(t, upstream.backend)
}

// The watcher replaces the config while requests read it. Run with -race.
func TestServer_LoadConfigWhileServing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := NewServer(logger, false)
	require.NoError(t, err)
	s.Register(configChangeTestPath, func(*filterapi.RuntimeConfig, map[string]string, *slog.Logger, bool, bool) (Processor, error) {
		return passThroughProcessor{}, nil
	})
	require.NoError(t, s.LoadConfig(t.Context(), &filterapi.Config{}))

	done := make(chan error)
	go func() {
		var loadErr error
		for range 100 {
			if loadErr == nil {
				loadErr = s.LoadConfig(t.Context(), &filterapi.Config{})
			}
		}
		done <- loadErr
	}()
	headers := map[string]string{":path": configChangeTestPath}
	for range 100 {
		_, err = s.processorForPath(headers, false, logger)
		require.NoError(t, err)
	}
	require.NoError(t, <-done)
}
