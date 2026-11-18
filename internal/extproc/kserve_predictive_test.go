// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package extproc

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/filterapi"
)

func TestIsKServePredictivePath(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/models/iris:predict":                    true,
		"/ep-1/v1/models/iris:predict":               true,
		"/v1/models/iris:explain":                    true,
		"/v2/models/iris/infer":                      true,
		"/ep-1/v2/models/iris/versions/3/infer":      true,
		"/v1/models":                                 false,
		"/v1/models/iris":                            false,
		"/v1/models/a/b:predict":                     false,
		"/v2/models/iris/ready":                      false,
		"/v1/chat/completions":                       false,
		"/v1/models/iris:predict/../../../v1/rerank": false,
	} {
		require.Equal(t, want, isKServePredictivePath(path), path)
	}
}

func TestProcessorForPathPassesPredictiveThrough(t *testing.T) {
	s, err := NewServer(slog.Default(), false)
	require.NoError(t, err)
	s.config = &filterapi.RuntimeConfig{}
	p, err := s.processorForPath(map[string]string{":path": "/ep-1/v1/models/iris:predict"}, false, slog.Default())
	require.NoError(t, err)
	require.IsType(t, passThroughProcessor{}, p)
	_, err = s.processorForPath(map[string]string{":path": "/ep-1/metrics"}, false, slog.Default())
	require.ErrorIs(t, err, errNoProcessor)
}
