// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package requestheaderattrs

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestResolveAll(t *testing.T) {
	tests := []struct {
		name            string
		base            *string
		span            *string
		metrics         *string
		log             *string
		expectedSpan    map[string]string
		expectedMetrics map[string]string
		expectedLog     map[string]string
		expectedErr     string
	}{
		{
			name:         "defaults apply to span/log only",
			expectedSpan: map[string]string{"agent-session-id": "session.id"},
			expectedLog:  map[string]string{"agent-session-id": "session.id"},
		},
		{
			name:            "google API key is excluded from every telemetry sink",
			base:            ptr.To("x-goog-api-key:credential,x-tenant-id:tenant.id"),
			span:            ptr.To("X-Goog-Api-Key:span.secret"),
			metrics:         ptr.To("x-goog-api-key:metrics.secret"),
			log:             ptr.To("x-goog-api-key:log.secret"),
			expectedSpan:    map[string]string{"x-tenant-id": "tenant.id"},
			expectedMetrics: map[string]string{"x-tenant-id": "tenant.id"},
			expectedLog:     map[string]string{"x-tenant-id": "tenant.id"},
		},
		{
			name:            "base merges into metrics and span/log",
			base:            ptr.To("x-tenant-id:tenant.id"),
			expectedMetrics: map[string]string{"x-tenant-id": "tenant.id"},
			expectedSpan: map[string]string{
				"agent-session-id": "session.id",
				"x-tenant-id":      "tenant.id",
			},
			expectedLog: map[string]string{
				"agent-session-id": "session.id",
				"x-tenant-id":      "tenant.id",
			},
		},
		{
			name:            "explicit empty span/log clears default but keeps base",
			base:            ptr.To("x-tenant-id:tenant.id"),
			span:            ptr.To(""),
			log:             ptr.To(""),
			expectedMetrics: map[string]string{"x-tenant-id": "tenant.id"},
			expectedSpan:    map[string]string{"x-tenant-id": "tenant.id"},
			expectedLog:     map[string]string{"x-tenant-id": "tenant.id"},
		},
		{
			name:         "explicit span/log override replaces default",
			span:         ptr.To("x-forwarded-proto:url.scheme"),
			log:          ptr.To("x-forwarded-proto:url.scheme"),
			expectedSpan: map[string]string{"x-forwarded-proto": "url.scheme"},
			expectedLog:  map[string]string{"x-forwarded-proto": "url.scheme"},
		},
		{
			name:        "invalid base returns error",
			base:        ptr.To("invalid"),
			expectedErr: "invalid request header attributes: invalid",
		},
		{
			name:        "invalid span returns error",
			span:        ptr.To("invalid"),
			expectedErr: "invalid span request header attributes: invalid",
		},
		{
			name:        "invalid metrics returns error",
			metrics:     ptr.To("invalid"),
			expectedErr: "invalid metrics request header attributes: invalid",
		},
		{
			name:        "invalid log returns error",
			log:         ptr.To("invalid"),
			expectedErr: "invalid log request header attributes: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanAttrs, metricsAttrs, logAttrs, err := ResolveAll(tt.base, tt.span, tt.metrics, tt.log)
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedMetrics, metricsAttrs)
			require.Equal(t, tt.expectedSpan, spanAttrs)
			require.Equal(t, tt.expectedLog, logAttrs)
		})
	}
}

func TestResolveLog(t *testing.T) {
	tests := []struct {
		name        string
		base        *string
		log         *string
		expected    map[string]string
		expectedErr string
	}{
		{
			name: "base merges into defaults",
			base: ptr.To("x-tenant-id:tenant.id"),
			expected: map[string]string{
				"agent-session-id": "session.id",
				"x-tenant-id":      "tenant.id",
			},
		},
		{
			name: "explicit empty clears default",
			log:  ptr.To(""),
		},
		{
			name:        "invalid base returns error",
			base:        ptr.To("invalid"),
			expectedErr: "invalid request header attributes: invalid",
		},
		{
			name:        "invalid log returns error",
			log:         ptr.To("invalid"),
			expectedErr: "invalid log request header attributes: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logAttrs, err := ResolveLog(tt.base, tt.log)
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, logAttrs)
		})
	}
}
