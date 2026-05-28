// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func requirePathHeader(t *testing.T, headers []internalapi.Header, expectedPath string) {
	t.Helper()
	var found bool
	for _, h := range headers {
		if h.Key() == pathHeaderName {
			found = true
			require.Equal(t, expectedPath, h.Value())
		}
	}
	require.True(t, found, "expected :path header to be present")
}
