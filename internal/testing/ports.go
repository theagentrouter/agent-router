// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package internaltesting

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// minTestPort and maxTestPort bound the ports RequireRandomPorts returns. They sit below the
// ephemeral port ranges (32768+ on Linux, 49152+ on macOS), so the OS never assigns them to
// outgoing connections or other sockets that bind port 0.
const (
	minTestPort = 10000
	maxTestPort = 32767
)

// RequireRandomPorts returns random available ports.
//
// Each port is checked on all interfaces, as the services under test bind it. On macOS, a port
// that is free on 127.0.0.1 can still be in use on the wildcard address.
func RequireRandomPorts(t testing.TB, count int) []int {
	t.Helper()
	return requireRandomPortsInRange(t, count, minTestPort, maxTestPort)
}

// requireRandomPortsInRange returns count ports in [first, last] that are free on all interfaces.
func requireRandomPortsInRange(t testing.TB, count, first, last int) []int {
	t.Helper()

	size := last - first + 1
	offset, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
	require.NoError(t, err)

	ports := make([]int, 0, count)
	var listeners []net.Listener
	for i := 0; i < size && len(ports) < count; i++ {
		port := first + (int(offset.Int64())+i)%size
		lc := net.ListenConfig{}
		lis, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			continue // In use.
		}
		listeners = append(listeners, lis)
		ports = append(ports, port)
	}
	for _, lis := range listeners {
		require.NoError(t, lis.Close())
	}
	require.Len(t, ports, count, "not enough free ports in [%d, %d]", first, last)
	return ports
}

// AwaitPortClosed waits until the port is no longer listening.
func AwaitPortClosed(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil // Port closed
		}
		conn.Close()

		if time.Now().After(deadline) {
			return fmt.Errorf("port %d still listening after %v", port, timeout)
		}
		<-ticker.C
	}
}
