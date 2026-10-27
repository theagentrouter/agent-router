// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"log/slog"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/apischema/anthropic"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

func TestModels_ProcessRequestHeaders(t *testing.T) {
	now := time.Now()
	cfg := &filterapi.RuntimeConfig{DeclaredModels: []filterapi.Model{
		{
			Name:      "openai",
			OwnedBy:   "openai",
			CreatedAt: now,
		},
		{
			Name:      "aws-bedrock",
			OwnedBy:   "aws",
			CreatedAt: now,
		},
	}}
	p, err := NewModelsProcessor(cfg, nil, slog.Default(), false, false)
	require.NoError(t, err)
	res, err := p.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}},
	})
	require.NoError(t, err)

	ir, ok := res.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
	require.True(t, ok)
	require.Equal(t, typev3.StatusCode(200), ir.ImmediateResponse.Status.Code)
	require.Equal(t, uint32(0), ir.ImmediateResponse.GrpcStatus.Status)

	respHeaders := headers(ir.ImmediateResponse.Headers.SetHeaders)
	require.Equal(t, "application/json", respHeaders["content-type"])

	var models openai.ModelList
	require.NoError(t, json.Unmarshal(ir.ImmediateResponse.Body, &models))
	require.Equal(t, "list", models.Object)
	require.Len(t, models.Data, len(cfg.DeclaredModels))
	for i, m := range cfg.DeclaredModels {
		require.Equal(t, "model", models.Data[i].Object)
		require.Equal(t, m.Name, models.Data[i].ID)
		require.Equal(t, now.Unix(), time.Time(models.Data[i].Created).Unix())
		require.Equal(t, m.OwnedBy, models.Data[i].OwnedBy)
	}
}

func TestAnthropicModels_ProcessRequestHeaders(t *testing.T) {
	now := time.Now()
	cfg := &filterapi.RuntimeConfig{DeclaredModels: []filterapi.Model{
		{
			Name:      "claude-opus-4-8",
			OwnedBy:   "anthropic",
			CreatedAt: now,
		},
		{
			Name:      "aws-bedrock",
			OwnedBy:   "aws",
			CreatedAt: now,
		},
	}}
	p, err := NewAnthropicModelsProcessor(cfg, nil, slog.Default(), false, false)
	require.NoError(t, err)
	res, err := p.ProcessRequestHeaders(t.Context(), &corev3.HeaderMap{
		Headers: []*corev3.HeaderValue{{Key: "foo", Value: "bar"}},
	})
	require.NoError(t, err)

	ir, ok := res.Response.(*extprocv3.ProcessingResponse_ImmediateResponse)
	require.True(t, ok)
	require.Equal(t, typev3.StatusCode(200), ir.ImmediateResponse.Status.Code)
	require.Equal(t, uint32(0), ir.ImmediateResponse.GrpcStatus.Status)

	respHeaders := headers(ir.ImmediateResponse.Headers.SetHeaders)
	require.Equal(t, "application/json", respHeaders["content-type"])

	var models anthropic.ModelList
	require.NoError(t, json.Unmarshal(ir.ImmediateResponse.Body, &models))
	require.False(t, models.HasMore)
	require.Equal(t, "claude-opus-4-8", models.FirstID)
	require.Equal(t, "aws-bedrock", models.LastID)
	require.Len(t, models.Data, len(cfg.DeclaredModels))
	for i, m := range cfg.DeclaredModels {
		require.Equal(t, "model", models.Data[i].Type)
		require.Equal(t, m.Name, models.Data[i].ID)
		require.Equal(t, m.Name, models.Data[i].DisplayName)
		require.Equal(t, now.UTC().Format(time.RFC3339), models.Data[i].CreatedAt)
	}
}

func headers(in []*corev3.HeaderValueOption) map[string]string {
	h := make(map[string]string)
	for _, v := range in {
		h[v.Header.Key] = string(v.Header.RawValue)
	}
	return h
}

func Test_requestHost(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name: "prefers :authority over host",
			headers: map[string]string{
				":authority": "Api.Bentoml.COM",
				"host":       "should-be-ignored.example.com",
			},
			want: "api.bentoml.com",
		},
		{
			name:    "falls back to host header when :authority is missing",
			headers: map[string]string{"host": "API.example.com"},
			want:    "api.example.com",
		},
		{
			name:    "strips port for IPv4 / hostname authorities",
			headers: map[string]string{":authority": "api.bentoml.com:8443"},
			want:    "api.bentoml.com",
		},
		{
			name: "strips port for bracketed IPv6 authority (the case net.SplitHostPort fixes)",
			// Before the SplitHostPort change, `strings.IndexByte(host, ':')` would have
			// truncated this to `[2001` and ToLower-ed it — a clearly broken result. Lock
			// the correct behaviour in.
			headers: map[string]string{":authority": "[2001:db8::1]:8443"},
			want:    "2001:db8::1",
		},
		{
			name:    "leaves unbracketed IPv6 alone (SplitHostPort fails, fallback returns the raw value lowercased)",
			headers: map[string]string{":authority": "2001:db8::1"},
			want:    "2001:db8::1",
		},
		{
			name:    "returns empty when neither :authority nor host is present",
			headers: map[string]string{},
			want:    "",
		},
		{
			name:    "nil headers map",
			headers: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, requestHost(tt.headers))
		})
	}
}

func Test_selectModelsForHost(t *testing.T) {
	defaultModels := []filterapi.Model{{Name: "default"}}
	unscopedModels := []filterapi.Model{{Name: "unscoped"}}
	exactModels := []filterapi.Model{{Name: "exact"}}
	wildcardComModels := []filterapi.Model{{Name: "wildcard-com"}}
	wildcardBentomlModels := []filterapi.Model{{Name: "wildcard-bentoml"}}

	cfg := &filterapi.RuntimeConfig{
		DeclaredModels: defaultModels,
		UnscopedModels: unscopedModels,
		ModelsByHost: map[string][]filterapi.Model{
			"api.bentoml.com":   exactModels,
			"*.com":             wildcardComModels,
			"*.bentoml.com":     wildcardBentomlModels,
			"not-a-pattern.com": {{Name: "ignored"}},
		},
	}

	tests := []struct {
		name string
		host string
		want []filterapi.Model
	}{
		{
			name: "exact match wins",
			host: "api.bentoml.com",
			want: exactModels,
		},
		{
			name: "wildcard match with label boundary",
			host: "chat.bentoml.com",
			want: wildcardBentomlModels,
		},
		{
			name: "more specific wildcard wins",
			host: "foo.bentoml.com",
			want: wildcardBentomlModels,
		},
		{
			name: "wildcard does not match apex",
			host: "bentoml.com",
			want: wildcardComModels,
		},
		{
			name: "wildcard does not match missing boundary",
			host: "evilbentoml.com",
			want: wildcardComModels,
		},
		{
			// Per Gateway API spec, "*.bentoml.com" matches exactly one label, so a
			// multi-label subdomain ("foo.api.bentoml.com") must NOT match it. "*.com"
			// likewise must not match it. With no wildcard matching, we fall back to
			// the unscoped models.
			name: "wildcard does not match multi-label subdomain",
			host: "foo.api.bentoml.com",
			want: unscopedModels,
		},
		{
			name: "unmatched host falls back to unscoped models",
			host: "localhost",
			want: unscopedModels,
		},
		{
			name: "empty host falls back to declared (all) models",
			host: "",
			want: defaultModels,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, selectModelsForHost(tt.host, cfg))
		})
	}

	t.Run("no unscoped models: unmatched host returns nil", func(t *testing.T) {
		// When every route is hostname-scoped, UnscopedModels is nil and an unmatched
		// host returns nil (treated as empty by the /v1/models endpoint). This preserves
		// the "no leak to unknown hosts" guarantee.
		scopedOnly := &filterapi.RuntimeConfig{
			DeclaredModels: defaultModels,
			ModelsByHost: map[string][]filterapi.Model{
				"api.bentoml.com": exactModels,
			},
		}
		require.Nil(t, selectModelsForHost("localhost", scopedOnly))
	})
}
