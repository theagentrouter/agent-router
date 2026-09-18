// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractGuardrailContents(t *testing.T) {
	contents, err := extractGuardrailContents([]byte(`{
		"model":"must-not-be-scanned",
		"messages":[
			{"role":"system","content":"system text"},
			{"role":"user","content":[{"type":"text","text":"user text"},{"type":"image_url","image_url":{"url":"https://example.com"}}]}
		]
	}`))
	require.NoError(t, err)
	require.Equal(t, []guardrailContent{
		{text: []byte("system text"), path: "messages.0.content"},
		{text: []byte("user text"), path: "messages.1.content.0.text"},
	}, contents)
}
