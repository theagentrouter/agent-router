// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPassthroughTranslatorOverridesModelAndKeepsPath(t *testing.T) {
	tr := NewPassthroughTranslator("served-name")
	headers, body, err := tr.RequestBody([]byte(`{"model":"alias","query":"q","documents":["a"]}`), &PassthroughRequest{Model: "alias"}, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"served-name","query":"q","documents":["a"]}`, string(body))
	for _, h := range headers {
		require.NotEqual(t, pathHeaderName, h.Key(), "passthrough must not override the routed path")
	}
}

func TestPassthroughTranslatorWithoutOverrideLeavesBody(t *testing.T) {
	tr := NewPassthroughTranslator("")
	headers, body, err := tr.RequestBody([]byte(`{"model":"m"}`), &PassthroughRequest{Model: "m"}, false)
	require.NoError(t, err)
	require.Nil(t, body)
	require.Empty(t, headers)
}

func TestPassthroughTranslatorUsage(t *testing.T) {
	cases := map[string]struct {
		body                 string
		input, output, total uint32
		hasInput, hasOutput  bool
	}{
		"chat batch": {body: `{"model":"m","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":30,"total_tokens":42}}`, input: 12, output: 30, total: 42, hasInput: true, hasOutput: true},
		"rerank":     {body: `{"model":"m","results":[],"usage":{"total_tokens":17}}`, input: 17, total: 17, hasInput: true},
		"score":      {body: `{"data":[],"usage":{"prompt_tokens":9,"total_tokens":9}}`, input: 9, total: 9, hasInput: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tr := NewPassthroughTranslator("")
			_, _, err := tr.RequestBody([]byte(`{"model":"m"}`), &PassthroughRequest{Model: "m"}, false)
			require.NoError(t, err)
			_, body, usage, model, err := tr.ResponseBody(nil, strings.NewReader(tc.body), true, nil)
			require.NoError(t, err)
			require.Nil(t, body)
			require.Equal(t, "m", model)
			in, ok := usage.InputTokens()
			require.Equal(t, tc.hasInput, ok)
			require.Equal(t, tc.input, in)
			out, ok := usage.OutputTokens()
			require.Equal(t, tc.hasOutput, ok)
			require.Equal(t, tc.output, out)
			total, _ := usage.TotalTokens()
			require.Equal(t, tc.total, total)
		})
	}
}

func TestPassthroughTranslatorToleratesBodiesWithoutUsage(t *testing.T) {
	tr := NewPassthroughTranslator("")
	_, _, usage, _, err := tr.ResponseBody(nil, strings.NewReader(`{"count":3,"tokens":[1,2,3]}`), true, nil)
	require.NoError(t, err)
	_, ok := usage.TotalTokens()
	require.False(t, ok)
	_, _, _, _, err = tr.ResponseBody(nil, strings.NewReader(`not json`), true, nil)
	require.NoError(t, err)
}
