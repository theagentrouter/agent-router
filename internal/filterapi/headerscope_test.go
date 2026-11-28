// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package filterapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalHeaderScopeKey(t *testing.T) {
	tests := []struct {
		name  string
		pairs map[string]string
		want  string
	}{
		{
			name:  "empty",
			pairs: nil,
			want:  "",
		},
		{
			name:  "single header",
			pairs: map[string]string{"x-jwt-sub": "client-a"},
			want:  "x-jwt-sub=client-a",
		},
		{
			name: "multiple headers sorted",
			pairs: map[string]string{
				"x-tenant-id": "tenant-1",
				"x-jwt-sub":   "client-a",
			},
			want: "x-jwt-sub=client-a,x-tenant-id=tenant-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, CanonicalHeaderScopeKey(tt.pairs))
		})
	}
}

func TestRequestHeaderScopeKey(t *testing.T) {
	modelsByScope := map[string][]Model{
		"x-jwt-sub=client-a": {{Name: "m1"}},
		"x-jwt-sub=client-b": {{Name: "m2"}},
	}

	require.Equal(t, "x-jwt-sub=client-a", RequestHeaderScopeKey(modelsByScope, map[string]string{
		"X-JWT-Sub": "client-a",
	}))
	require.Equal(t, "x-jwt-sub=unknown", RequestHeaderScopeKey(modelsByScope, map[string]string{
		"x-jwt-sub": "unknown",
	}))
	require.Empty(t, RequestHeaderScopeKey(modelsByScope, nil))
}
