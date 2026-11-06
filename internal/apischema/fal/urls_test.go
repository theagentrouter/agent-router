// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package fal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testRunBase   = "https://gw.example/fal/run/"
	testQueueBase = "https://gw.example/fal/queue/"
)

func TestGatewayBases(t *testing.T) {
	tests := []struct {
		name         string
		originalPath string
		authority    string
		scheme       string
		expRun       string
		expQueue     string
		expOK        bool
	}{
		{
			name:         "bases derived from a queue client path",
			originalPath: "/fal/queue/fal-ai/flux/dev?fal_webhook=x",
			authority:    "gw.example",
			scheme:       "https",
			expRun:       testRunBase,
			expQueue:     testQueueBase,
			expOK:        true,
		},
		{
			name:         "root prefix kept",
			originalPath: "/gw/fal/run/fal-ai/flux/dev",
			authority:    "gw.example:8443",
			scheme:       "http",
			expRun:       "http://gw.example:8443/gw/fal/run/",
			expQueue:     "http://gw.example:8443/gw/fal/queue/",
			expOK:        true,
		},
		{name: "no fal path", originalPath: "/v1/chat/completions", authority: "gw.example", scheme: "https"},
		{name: "no authority", originalPath: "/fal/run/fal-ai/flux/dev", scheme: "https"},
		{name: "no scheme", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example"},
		{name: "scheme with a quote", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example", scheme: `https","x":"`},
		{name: "scheme that is not http or https", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example", scheme: "ftp"},
		{
			name:         "IPv6 authority with port",
			originalPath: "/fal/run/fal-ai/flux/dev",
			authority:    "[::1]:8443",
			scheme:       "http",
			expRun:       "http://[::1]:8443/fal/run/",
			expQueue:     "http://[::1]:8443/fal/queue/",
			expOK:        true,
		},
		{name: "authority with userinfo", originalPath: "/fal/run/fal-ai/flux/dev", authority: "u@gw.example", scheme: "https"},
		{name: "authority with a path", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example/x", scheme: "https"},
		{name: "authority with a quote", originalPath: "/fal/run/fal-ai/flux/dev", authority: `gw.example",`, scheme: "https"},
		{name: "authority with a space", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw example", scheme: "https"},
		{name: "authority with a non-numeric port", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example:8a", scheme: "https"},
		{name: "authority with an empty port", originalPath: "/fal/run/fal-ai/flux/dev", authority: "gw.example:", scheme: "https"},
		{name: "unterminated IPv6 authority", originalPath: "/fal/run/fal-ai/flux/dev", authority: "[::1", scheme: "https"},
		{name: "IPv6 authority followed by a quote", originalPath: "/fal/run/fal-ai/flux/dev", authority: `[::1]"1`, scheme: "https"},
		{name: "IPv6 authority followed by a backslash", originalPath: "/fal/run/fal-ai/flux/dev", authority: `[::1]\1`, scheme: "https"},
		{name: "IPv6 authority followed by junk", originalPath: "/fal/run/fal-ai/flux/dev", authority: "[::1]x80", scheme: "https"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, queue, ok := GatewayBases("/fal", tt.originalPath, tt.authority, tt.scheme)
			require.Equal(t, tt.expOK, ok)
			require.Equal(t, tt.expRun, run)
			require.Equal(t, tt.expQueue, queue)
		})
	}
}

func TestGatewayBases_Prefix(t *testing.T) {
	tests := []struct {
		name         string
		prefix       string
		originalPath string
		expRun       string
		expQueue     string
		expOK        bool
	}{
		{
			name:         "default prefix",
			prefix:       "/fal",
			originalPath: "/fal/queue/fal-ai/flux/dev",
			expRun:       testRunBase,
			expQueue:     testQueueBase,
			expOK:        true,
		},
		{
			name:         "custom prefix",
			prefix:       "/gen",
			originalPath: "/gen/queue/fal-ai/flux/dev?fal_webhook=x",
			expRun:       "https://gw.example/gen/run/",
			expQueue:     "https://gw.example/gen/queue/",
			expOK:        true,
		},
		{
			name:         "custom prefix under root prefix",
			prefix:       "/gen",
			originalPath: "/gw/gen/run/fal-ai/flux/dev",
			expRun:       "https://gw.example/gw/gen/run/",
			expQueue:     "https://gw.example/gw/gen/queue/",
			expOK:        true,
		},
		{
			name:         "empty prefix",
			prefix:       "",
			originalPath: "/run/fal-ai/flux/dev",
			expRun:       "https://gw.example/run/",
			expQueue:     "https://gw.example/queue/",
			expOK:        true,
		},
		{name: "default path under custom prefix", prefix: "/gen", originalPath: "/fal/run/fal-ai/flux/dev"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, queue, ok := GatewayBases(tt.prefix, tt.originalPath, "gw.example", "https")
			require.Equal(t, tt.expOK, ok)
			require.Equal(t, tt.expRun, run)
			require.Equal(t, tt.expQueue, queue)
		})
	}
}

func TestRewriteURLs(t *testing.T) {
	tests := []struct {
		name string
		body string
		exp  string
	}{
		{
			name: "submit response urls rewritten",
			body: `{"request_id":"764cabcf","response_url":"https://queue.fal.run/fal-ai/flux/requests/764cabcf","status_url":"https://queue.fal.run/fal-ai/flux/requests/764cabcf/status","cancel_url":"https://queue.fal.run/fal-ai/flux/requests/764cabcf/cancel","queue_position":0}`,
			exp:  `{"request_id":"764cabcf","response_url":"https://gw.example/fal/queue/fal-ai/flux/requests/764cabcf","status_url":"https://gw.example/fal/queue/fal-ai/flux/requests/764cabcf/status","cancel_url":"https://gw.example/fal/queue/fal-ai/flux/requests/764cabcf/cancel","queue_position":0}`,
		},
		{
			name: "status response urls rewritten",
			body: `{"status":"IN_QUEUE","request_id":"764cabcf","queue_position":2,"response_url":"https://queue.fal.run/fal-ai/flux/dev/requests/764cabcf?logs=1"}`,
			exp:  `{"status":"IN_QUEUE","request_id":"764cabcf","queue_position":2,"response_url":"https://gw.example/fal/queue/fal-ai/flux/dev/requests/764cabcf?logs=1"}`,
		},
		{
			name: "SSE status event rewritten",
			body: "data: {\"status\":\"COMPLETED\",\"response_url\":\"https://queue.fal.run/fal-ai/flux/requests/764cabcf\"}\n\n",
			exp:  "data: {\"status\":\"COMPLETED\",\"response_url\":\"https://gw.example/fal/queue/fal-ai/flux/requests/764cabcf\"}\n\n",
		},
		{
			name: "fal.run url rewritten",
			body: `{"next":"https://fal.run/fal-ai/x/stream"}`,
			exp:  `{"next":"https://gw.example/fal/run/fal-ai/x/stream"}`,
		},
		{
			name: "non-fal URLs and media URLs untouched",
			body: `{"images":[{"url":"https://v3.fal.media/files/a.png"}],"docs":"https://fal.ai/docs","other":"https://example.com/fal.run/x"}`,
			exp:  `{"images":[{"url":"https://v3.fal.media/files/a.png"}],"docs":"https://fal.ai/docs","other":"https://example.com/fal.run/x"}`,
		},
		{
			name: "body without urls unchanged",
			body: `{"status":"OK"}`,
			exp:  `{"status":"OK"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.exp, string(RewriteURLs([]byte(tt.body), testRunBase, testQueueBase)))
		})
	}
}

func TestSSERewriter(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		exp    string
	}{
		{
			name:   "url split across two chunks rewritten",
			chunks: []string{"data: {\"status_url\":\"https://queue.fa", "l.run/fal-ai/flux/requests/r/status\"}\n\n"},
			exp:    "data: {\"status_url\":\"https://gw.example/fal/queue/fal-ai/flux/requests/r/status\"}\n\n",
		},
		{
			name:   "partial host at end of stream flushed",
			chunks: []string{"data: {\"status\":\"COMPLETED\"}\n\nhttps://fal"},
			exp:    "data: {\"status\":\"COMPLETED\"}\n\nhttps://fal",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewSSERewriter(testRunBase, testQueueBase)
			var out []byte
			for i, c := range tt.chunks {
				out = append(out, r.Write([]byte(c), i == len(tt.chunks)-1)...)
			}
			require.Equal(t, tt.exp, string(out))
		})
	}
}
