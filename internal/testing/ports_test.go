// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireRandomPorts(t *testing.T) {
	ports := RequireRandomPorts(t, 6)
	require.Len(t, ports, 6)

	seen := make(map[int]bool, len(ports))
	for _, port := range ports {
		require.GreaterOrEqual(t, port, minTestPort)
		require.LessOrEqual(t, port, maxTestPort)
		require.False(t, seen[port], "port %d returned twice", port)
		seen[port] = true

		lis := listenOnAllInterfaces(t, port)
		require.NoError(t, lis.Close())
	}
}

func TestRequireRandomPortsInRange_skipsPortInUse(t *testing.T) {
	busy := RequireRandomPorts(t, 1)[0]
	lis := listenOnAllInterfaces(t, busy)
	t.Cleanup(func() { _ = lis.Close() })

	require.Equal(t, []int{busy + 1}, requireRandomPortsInRange(t, 1, busy, busy+1))
}

func listenOnAllInterfaces(t *testing.T, port int) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(t.Context(), "tcp", fmt.Sprintf(":%d", port))
	require.NoError(t, err)
	return lis
}
