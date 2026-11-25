// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package controller

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShortStableHash(t *testing.T) {
	h1 := shortStableHash("default/gw")
	h2 := shortStableHash("default/gw")
	h3 := shortStableHash("default/gw-other")

	require.Equal(t, h1, h2)
	require.NotEqual(t, h1, h3)
	require.Len(t, h1, 12)
}

func TestTruncateAndAppendHash(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		hash    string
		maxLen  int
		expect  string
		wantLen int
	}{
		{
			name:    "fits max length",
			base:    "gw-default",
			hash:    "abc123",
			maxLen:  64,
			expect:  "gw-default-abc123",
			wantLen: len("gw-default-abc123"),
		},
		{
			name:    "max too small returns hash only",
			base:    "gw-default",
			hash:    "abc123",
			maxLen:  6,
			expect:  "abc123",
			wantLen: len("abc123"),
		},
		{
			name:    "trims base to fit max",
			base:    strings.Repeat("a", 40),
			hash:    "abc123",
			maxLen:  16,
			expect:  "aaaaaaaaa-abc123",
			wantLen: 16,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateAndAppendHash(tt.base, tt.hash, tt.maxLen)
			require.Equal(t, tt.expect, got)
			require.Len(t, got, tt.wantLen)
		})
	}
}

func TestFilterConfigBundleIndexSecretName(t *testing.T) {
	tests := []struct {
		name        string
		gwName      string
		gwNamespace string
		expect      string
	}{
		{
			name:        "short gateway name",
			gwName:      "gw",
			gwNamespace: "default",
			expect:      "gw-default-3d45476e8d68",
		},
		{
			name:        "another identity",
			gwName:      "gateway",
			gwNamespace: "ns1",
			expect:      "gateway-ns1-c6d39be275c7",
		},
		{
			name:        "long gateway name bounded with hash",
			gwName:      strings.Repeat("gw", 300),
			gwNamespace: "default",
			expect:      strings.Repeat("gw", 120) + "-5751046a2499",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterConfigBundleIndexSecretName(tt.gwName, tt.gwNamespace)
			require.Equal(t, tt.expect, got)
			require.LessOrEqual(t, len(got), k8sObjectNameMaxLen)
			require.Contains(t, got, shortStableHash(tt.gwNamespace+"/"+tt.gwName))
		})
	}
}

func TestFilterConfigBundlePartSecretName(t *testing.T) {
	tests := []struct {
		name        string
		gwName      string
		gwNamespace string
		idx         int
		expect      string
	}{
		{
			name:        "short hashed base",
			gwName:      "gw",
			gwNamespace: "default",
			idx:         0,
			expect:      "gw-default-3d45476e8d68-part-000",
		},
		{
			name:        "hashed base with long prefix keeps full hash",
			gwName:      strings.Repeat("gw", 200),
			gwNamespace: "default",
			idx:         5,
			expect:      strings.Repeat("gw", 115) + "g-32c5e16657f8-part-005",
		},
		{
			name:        "real index name from gateway identity",
			gwName:      "gw",
			gwNamespace: "default",
			idx:         7,
			expect:      "gw-default-3d45476e8d68-part-007",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterConfigBundlePartSecretName(tt.gwName, tt.gwNamespace, tt.idx)
			require.Equal(t, tt.expect, got)
			require.LessOrEqual(t, len(got), k8sObjectNameMaxLen)
			require.Contains(t, got, shortStableHash(tt.gwNamespace+"/"+tt.gwName))
		})
	}
}

func TestFilterConfigBundleVolumeName(t *testing.T) {
	tests := []struct {
		name        string
		gwName      string
		gwNamespace string
		expect      string
	}{
		{
			name:        "short gateway name",
			gwName:      "gw",
			gwNamespace: "default",
			expect:      "ai-gateway-gw-default-3d45476e8d68-bundle",
		},
		{
			name:        "another identity",
			gwName:      "gateway",
			gwNamespace: "ns1",
			expect:      "ai-gateway-gateway-ns1-c6d39be275c7-bundle",
		},
		{
			name:        "long gateway name bounded with hash",
			gwName:      strings.Repeat("gw", 50),
			gwNamespace: "default",
			expect:      "ai-gateway-gwgwgwgwgwgwgwgwgwgwgwgwgwgwgwgw-aa314d0b5069-bundle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterConfigBundleVolumeName(tt.gwName, tt.gwNamespace)
			require.Equal(t, tt.expect, got)
			require.LessOrEqual(t, len(got), k8sVolumeNameMaxLen)
			require.True(t, strings.HasSuffix(got, "-bundle"))
		})
	}
}
