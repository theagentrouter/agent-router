// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseGeminiGenerateContentPath(t *testing.T) {
	const prefix = "/gateway/gemini/v1beta/models"
	for _, tc := range []struct {
		path, upstream string
		streaming      bool
	}{
		{"/gemini/v1beta/models/alias:generateContent", "", false},
		{prefix + "/alias:countTokens", "", false},
		{prefix + "/alias/other:generateContent", "", false},
		{prefix + "/%2f:generateContent", "", false},
		{prefix + "/alias:generateContent?alt=proto", "", false},
		{prefix + "/alias:generateContent", "/v1beta/models/served:generateContent", false},
		{prefix + "/alias:streamGenerateContent?key=discard&%24alt=sse", "/v1beta/models/served:streamGenerateContent?alt=sse", true},
		{prefix + "/alias:streamGenerateContent", "/v1beta/models/served:streamGenerateContent", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path, err := ParseGeminiGenerateContentPath(prefix, tc.path)
			if tc.upstream == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "alias", path.Model)
			require.Equal(t, tc.streaming, path.Streaming())
			require.Equal(t, tc.upstream, path.Path("/v1beta/models", "served"))
		})
	}
}
