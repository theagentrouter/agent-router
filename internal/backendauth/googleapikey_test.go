// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package backendauth

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
)

func TestGoogleAPIKeyHandler(t *testing.T) {
	handler, err := NewHandler(t.Context(), &filterapi.BackendAuth{
		GoogleAPIKey: &filterapi.GoogleAPIKeyAuth{Key: "  configured-key  "},
	})
	require.NoError(t, err)

	headers := map[string]string{internalapi.GoogleAPIKeyHeader: "incoming-key"}
	mutations, err := handler.Do(t.Context(), headers, nil)
	require.NoError(t, err)
	require.Equal(t, "configured-key", headers[internalapi.GoogleAPIKeyHeader])
	require.Equal(t, []internalapi.Header{{internalapi.GoogleAPIKeyHeader, "configured-key"}}, mutations)
}
