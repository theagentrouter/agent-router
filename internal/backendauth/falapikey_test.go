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

func TestFalAPIKeyHandler(t *testing.T) {
	tests := []struct {
		name string
		key  string
		exp  string
	}{
		{name: "sets Authorization Key", key: "fal-key", exp: "Key fal-key"},
		{name: "trims whitespace", key: "  fal-key\n", exp: "Key fal-key"},
		{name: "keeps colon form", key: "id:secret", exp: "Key id:secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := newFalAPIKeyHandler(&filterapi.FalAPIKeyAuth{Key: tt.key})
			require.NoError(t, err)

			headers := map[string]string{"Authorization": "Bearer client"}
			hdrs, err := handler.Do(t.Context(), headers, nil)
			require.NoError(t, err)
			require.Equal(t, tt.exp, headers["Authorization"])
			require.Equal(t, []internalapi.Header{{"Authorization", tt.exp}}, hdrs)
		})
	}
}
