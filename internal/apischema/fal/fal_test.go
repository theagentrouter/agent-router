// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package fal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		exp   Path
		expOK bool
	}{
		{
			name:  "run",
			path:  "/fal/run/fal-ai/flux/dev",
			exp:   Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK: true,
		},
		{
			name:  "run with nested endpoint and query",
			path:  "/fal/run/fal-ai/kling-video/v2/master/text-to-video?fal_webhook=x",
			exp:   Path{Mode: ModeRun, Billed: true, Model: "fal-ai/kling-video/v2/master/text-to-video", Upstream: "/fal-ai/kling-video/v2/master/text-to-video?fal_webhook=x"},
			expOK: true,
		},
		{
			name:  "run stream",
			path:  "/fal/run/fal-ai/flux/dev/stream",
			exp:   Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Stream: true, Upstream: "/fal-ai/flux/dev/stream"},
			expOK: true,
		},
		{
			name:  "under root prefix",
			path:  "/gw/fal/run/fal-ai/flux/dev",
			exp:   Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK: true,
		},
		{
			name:  "queue submit",
			path:  "/fal/queue/fal-ai/flux/dev",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK: true,
		},
		{
			name:  "queue status is attributed to the app",
			path:  "/fal/queue/fal-ai/flux/requests/7f1c/status?logs=1",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux", Poll: true, Upstream: "/fal-ai/flux/requests/7f1c/status?logs=1"},
			expOK: true,
		},
		{
			name:  "queue status stream",
			path:  "/fal/queue/fal-ai/flux/requests/7f1c/status/stream",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux", Poll: true, Stream: true, Upstream: "/fal-ai/flux/requests/7f1c/status/stream"},
			expOK: true,
		},
		{
			name:  "queue result",
			path:  "/fal/queue/fal-ai/flux/requests/7f1c",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux", Poll: true, Billed: true, Upstream: "/fal-ai/flux/requests/7f1c"},
			expOK: true,
		},
		{
			name:  "queue cancel",
			path:  "/fal/queue/fal-ai/flux/requests/7f1c/cancel",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux", Poll: true, Upstream: "/fal-ai/flux/requests/7f1c/cancel"},
			expOK: true,
		},
		{name: "run without endpoint", path: "/fal/run/"},
		{name: "unknown mode", path: "/fal/other/fal-ai/flux"},
		{name: "not fal", path: "/v1/chat/completions"},
		{name: "empty segment", path: "/fal/run/fal-ai//flux"},
		{name: "dot segment", path: "/fal/run/fal-ai/../flux"},
		{name: "poll without app", path: "/fal/queue/requests/7f1c"},
		{name: "poll without request id", path: "/fal/queue/fal-ai/flux/requests/"},
		{name: "poll with an unknown tail", path: "/fal/queue/fal-ai/flux/requests/7f1c/other"},
		{name: "poll result with a trailing slash", path: "/fal/queue/fal-ai/flux/requests/7f1c/"},
		{name: "poll status with a trailing slash", path: "/fal/queue/fal-ai/flux/requests/7f1c/status/"},
		{name: "poll status in another case", path: "/fal/queue/fal-ai/flux/requests/7f1c/Status"},
		{name: "requests in another case is still the queue namespace", path: "/fal/queue/fal-ai/flux/Requests/7f1c/other"},
		{name: "requests in another case inside a run endpoint", path: "/fal/run/fal-ai/flux/Requests/7f1c"},
		{name: "poll with a path after cancel", path: "/fal/queue/fal-ai/flux/requests/7f1c/cancel/x"},
		{name: "poll stream without status", path: "/fal/queue/fal-ai/flux/requests/7f1c/stream"},
		{name: "poll request id with a slash-free escape", path: "/fal/queue/fal-ai/flux/requests/7f%2F1c"},
		{name: "poll request id too long", path: "/fal/queue/fal-ai/flux/requests/" + strings.Repeat("a", MaxRequestIDLength+1)},
		{name: "stream only", path: "/fal/run/stream"},
		{name: "one segment endpoint", path: "/fal/run/flux"},
		{name: "percent encoded segment", path: "/fal/run/fal-ai/%2e%2e/flux"},
		{name: "space in segment", path: "/fal/run/fal-ai/fl ux"},
		{name: "colon in segment", path: "/fal/run/fal-ai/flux:latest"},
		{name: "non ascii segment", path: "/fal/run/fal-ai/flüx"},
		{name: "leading dot segment", path: "/fal/run/fal-ai/.flux"},
		{name: "endpoint id too long", path: "/fal/run/fal-ai/" + strings.Repeat("a", MaxEndpointIDLength)},
		{name: "requests inside a run endpoint", path: "/fal/run/fal-ai/flux/requests/7f1c"},
		{
			name:  "endpoint with version and dashes",
			path:  "/fal/queue/fal-ai/flux-pro/v1.1",
			exp:   Path{Mode: ModeQueue, Model: "fal-ai/flux-pro/v1.1", Upstream: "/fal-ai/flux-pro/v1.1"},
			expOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParsePath("/fal", tt.path)
			require.Equal(t, tt.expOK, ok)
			require.Equal(t, tt.exp, got)
		})
	}
}

func TestParsePath_Prefix(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		path   string
		exp    Path
		expOK  bool
	}{
		{
			name:   "default prefix",
			prefix: "/fal",
			path:   "/fal/run/fal-ai/flux/dev",
			exp:    Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK:  true,
		},
		{
			name:   "custom prefix",
			prefix: "/gen",
			path:   "/gen/queue/fal-ai/flux/requests/7f1c/status",
			exp:    Path{Mode: ModeQueue, Model: "fal-ai/flux", Poll: true, Upstream: "/fal-ai/flux/requests/7f1c/status"},
			expOK:  true,
		},
		{
			name:   "custom prefix under root prefix",
			prefix: "/gen",
			path:   "/gw/gen/run/fal-ai/flux/dev/stream",
			exp:    Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Stream: true, Upstream: "/fal-ai/flux/dev/stream"},
			expOK:  true,
		},
		{
			name:   "prefix without leading slash",
			prefix: "gen/",
			path:   "/gen/run/fal-ai/flux/dev",
			exp:    Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK:  true,
		},
		{
			name:   "empty prefix",
			prefix: "",
			path:   "/run/fal-ai/flux/dev",
			exp:    Path{Mode: ModeRun, Billed: true, Model: "fal-ai/flux/dev", Upstream: "/fal-ai/flux/dev"},
			expOK:  true,
		},
		{name: "default path under custom prefix", prefix: "/gen", path: "/fal/run/fal-ai/flux/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParsePath(tt.prefix, tt.path)
			require.Equal(t, tt.expOK, ok)
			require.Equal(t, tt.exp, got)
		})
	}
}
