// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package dataplaneenv

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// The listener and ext_proc ports each begin with the other's placeholder (1062 and 1063), so a sequential
// replacement rewrites one of them a second time whichever order the tokens are applied in.
func TestReplaceTokensRendersRequestedPorts(t *testing.T) {
	const listener, admin, extProc, extProcMCP = 10637, 10638, 10620, 10621
	for _, tc := range []struct {
		name         string
		config       string
		miscDefaults map[string]int
	}{
		{name: "data-plane", config: "../../data-plane/envoy.yaml", miscDefaults: map[string]int{"upstream": 8080}},
		{name: "vcr", config: "../../data-plane/vcr/envoy.yaml", miscDefaults: map[string]int{"upstream": 11434}},
		{name: "data-plane-mcp", config: "../../data-plane-mcp/envoy.yaml", miscDefaults: map[string]int{"ts1": 8080, "ts2": 8081, "special_listener": 9999}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile(tc.config)
			require.NoError(t, err)

			miscPorts := map[string]int{}
			portFor := map[int]int{1062: listener, 9901: admin, 1063: extProc, 9856: extProcMCP}
			next := 10700
			for name, def := range tc.miscDefaults {
				miscPorts[name] = next
				portFor[def] = next
				next++
			}
			var expected []int
			for _, p := range portValues(t, string(raw)) {
				if port, ok := portFor[p]; ok {
					p = port
				}
				expected = append(expected, p)
			}

			rendered := replaceTokens(string(raw), envoyConfigReplacements(t, listener, admin, extProc, extProcMCP, miscPorts, tc.miscDefaults))
			require.Equal(t, expected, portValues(t, rendered))
		})
	}
}

var portValue = regexp.MustCompile(`port_value: (\d+)`)

// portValues returns every port_value in an Envoy config, in order.
func portValues(t *testing.T, config string) []int {
	t.Helper()
	var ports []int
	for _, m := range portValue.FindAllStringSubmatch(config, -1) {
		p, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		ports = append(ports, p)
	}
	return ports
}
