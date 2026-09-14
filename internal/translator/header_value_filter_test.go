// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package translator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCommaSeparatedHeader(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		header  string
		want    []string
	}{
		{name: "missing header", headers: map[string]string{}, header: "anthropic-beta", want: nil},
		{name: "empty header", headers: map[string]string{"anthropic-beta": ""}, header: "anthropic-beta", want: nil},
		{
			name:    "different header name is not read",
			headers: map[string]string{"anthropic-beta": "context-1m-2025-08-07"},
			header:  "x-other",
			want:    nil,
		},
		{
			name:    "single value",
			headers: map[string]string{"anthropic-beta": "context-1m-2025-08-07"},
			header:  "anthropic-beta",
			want:    []string{"context-1m-2025-08-07"},
		},
		{
			name:    "multiple values",
			headers: map[string]string{"anthropic-beta": "interleaved-thinking-2025-05-14,context-1m-2025-08-07"},
			header:  "anthropic-beta",
			want:    []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
		{
			name:    "whitespace and empty entries are trimmed and dropped",
			headers: map[string]string{"anthropic-beta": " interleaved-thinking-2025-05-14 , , context-1m-2025-08-07 "},
			header:  "anthropic-beta",
			want:    []string{"interleaved-thinking-2025-05-14", "context-1m-2025-08-07"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseCommaSeparatedHeader(tt.headers, tt.header))
		})
	}
}

func TestFilterHeaderValues(t *testing.T) {
	tests := []struct {
		name        string
		values      []string
		mode        string
		list        []string
		want        []string
		wantChanged bool
	}{
		{
			name:        "no filter configured",
			values:      []string{"advanced-tool-use-2025-11-20", "thinking-token-count-2026-05-13"},
			mode:        "",
			list:        nil,
			want:        []string{"advanced-tool-use-2025-11-20", "thinking-token-count-2026-05-13"},
			wantChanged: false,
		},
		{
			name:        "no values sent",
			values:      nil,
			mode:        "Denylist",
			list:        []string{"thinking-token-count-2026-05-13"},
			want:        nil,
			wantChanged: false,
		},
		{
			name:        "unknown mode leaves input unchanged",
			values:      []string{"advanced-tool-use-2025-11-20"},
			mode:        "bogus",
			list:        []string{"advanced-tool-use-2025-11-20"},
			want:        []string{"advanced-tool-use-2025-11-20"},
			wantChanged: false,
		},
		{
			// Modes are PascalCase; the lower-cased spelling must not silently enable filtering.
			name:        "mode is case-sensitive",
			values:      []string{"advanced-tool-use-2025-11-20"},
			mode:        "denylist",
			list:        []string{"advanced-tool-use-2025-11-20"},
			want:        []string{"advanced-tool-use-2025-11-20"},
			wantChanged: false,
		},
		{
			name:        "denylist drops the listed value, keeps the rest",
			values:      []string{"advanced-tool-use-2025-11-20", "thinking-token-count-2026-05-13"},
			mode:        "Denylist",
			list:        []string{"thinking-token-count-2026-05-13"},
			want:        []string{"advanced-tool-use-2025-11-20"},
			wantChanged: true,
		},
		{
			name:        "denylist with no match reports unchanged",
			values:      []string{"advanced-tool-use-2025-11-20"},
			mode:        "Denylist",
			list:        []string{"thinking-token-count-2026-05-13"},
			want:        []string{"advanced-tool-use-2025-11-20"},
			wantChanged: false,
		},
		{
			name:        "denylist can drop every value",
			values:      []string{"thinking-token-count-2026-05-13"},
			mode:        "Denylist",
			list:        []string{"thinking-token-count-2026-05-13"},
			want:        []string{},
			wantChanged: true,
		},
		{
			name:        "allowlist keeps only the listed value",
			values:      []string{"advanced-tool-use-2025-11-20", "thinking-token-count-2026-05-13"},
			mode:        "Allowlist",
			list:        []string{"advanced-tool-use-2025-11-20"},
			want:        []string{"advanced-tool-use-2025-11-20"},
			wantChanged: true,
		},
		{
			name:        "allowlist with nothing matching drops everything",
			values:      []string{"advanced-tool-use-2025-11-20"},
			mode:        "Allowlist",
			list:        []string{"thinking-token-count-2026-05-13"},
			want:        []string{},
			wantChanged: true,
		},
		{
			// Values are matched exactly, so a differently-cased value is a different value.
			name:        "value matching is case-sensitive",
			values:      []string{"Advanced-Tool-Use-2025-11-20"},
			mode:        "Denylist",
			list:        []string{"advanced-tool-use-2025-11-20"},
			want:        []string{"Advanced-Tool-Use-2025-11-20"},
			wantChanged: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotChanged := filterHeaderValues(tt.values, tt.mode, tt.list)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantChanged, gotChanged)
		})
	}
}
