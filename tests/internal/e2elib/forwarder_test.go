// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2elib

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// testPortForward implements portForward for testing.
// It maintains a persistent upstream server (simulating a Kubernetes pod) and
// a restartable proxy server (simulating kubectl port-forward tunnel).
type testPortForward struct {
	localURL, serviceURL     *url.URL
	proxy                    *httptest.Server
	proxyMu                  sync.Mutex
	startCount, failAttempts *atomic.Int32
}

func (m *testPortForward) start(ctx context.Context) error {
	if m.startCount != nil {
		m.startCount.Add(1)
	}

	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()

	// Create proxy server that forwards to serviceURL
	m.proxy = httptest.NewUnstartedServer(m.proxyHandler(m.serviceURL))

	// Close default listener and bind to predetermined localURL
	_ = m.proxy.Listener.Close()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", m.localURL.Host)
	if err != nil {
		return err
	}

	m.proxy.Listener = listener
	m.proxy.Start()

	return nil
}

func (m *testPortForward) kill() {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()

	if m.proxy != nil {
		m.proxy.Close() // Frees local port
		m.proxy = nil
	}
}

// proxyHandler returns a handler that acts as a reverse proxy to the serviceURL server.
// It simulates kubectl port-forward behavior by:
//   - Optionally failing POST requests by hijacking and closing the connection (simulates tunnel break)
//   - GET requests are never failed (they come from waitReady() and shouldn't trigger failures)
//   - Proxying all successful requests to the serviceURL server using httputil.ReverseProxy
func (m *testPortForward) proxyHandler(pod *url.URL) http.HandlerFunc {
	proxy := httputil.NewSingleHostReverseProxy(pod)

	return func(w http.ResponseWriter, r *http.Request) {
		// Simulate port-forward tunnel failure on POST requests (not GET from waitReady)
		if r.Method == http.MethodPost && m.failAttempts != nil && m.failAttempts.Load() > 0 {
			m.failAttempts.Add(-1)
			// Hijack connection and close it to simulate port-forward tunnel break
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
		}

		// Proxy request to serviceURL using stdlib reverse proxy
		proxy.ServeHTTP(w, r)
	}
}

// newTestPortForwarder creates a port forwarder with a test backend for testing.
// The podHandler runs on a persistent httptest server (simulating a Kubernetes serviceURL).
// The port-forward proxy can be killed and restarted without affecting the serviceURL.
func newTestPortForwarder(t *testing.T, podHandler http.HandlerFunc) PortForwarder {
	pod := httptest.NewServer(podHandler)
	t.Cleanup(pod.Close)
	servicePort := pod.Listener.Addr().(*net.TCPAddr).Port

	serviceURL, err := url.Parse(pod.URL)
	require.NoError(t, err)

	pf, err := newServicePortForwarder(t.Context(), func(_, _ string, localPort, _ int) portForward {
		return &testPortForward{
			localURL: &url.URL{
				Scheme: "http",
				Host:   fmt.Sprintf("127.0.0.1:%d", localPort),
			},
			serviceURL: serviceURL,
		}
	}, "test-ns", "test-sel", 0, servicePort)
	require.NoError(t, err)
	t.Cleanup(pf.Kill)

	return pf
}

func TestPortForwarder_Post(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		tunnelFailures      int32 // Number of times the port-forward tunnel fails
		podBehavior         http.HandlerFunc
		expectedResponse    string
		expectErrorContains string // If non-empty, error should contain this string
		expectStaleErr      bool
		expectedStarts      int32
	}{
		{
			name: "successful request",
			podBehavior: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("success"))
			},
			expectedStarts:   1,
			expectedResponse: "success",
		},
		{
			name:           "connection reset with retry",
			tunnelFailures: 1,
			podBehavior: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("success"))
			},
			expectedStarts:   2,
			expectedResponse: "success",
		},
		{
			name: "empty 500 with retry",
			podBehavior: func() http.HandlerFunc {
				var postCount atomic.Int32
				return func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						count := postCount.Add(1)
						if count == 1 {
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("success"))
				}
			}(),
			expectedStarts:   2,
			expectedResponse: "success",
		},
		{
			name:           "max retries exhausted",
			tunnelFailures: 10,
			podBehavior: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("success"))
			},
			expectStaleErr: true,
			expectedStarts: 6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := httptest.NewServer(tc.podBehavior)
			t.Cleanup(pod.Close)
			servicePort := pod.Listener.Addr().(*net.TCPAddr).Port

			serviceURL, err := url.Parse(pod.URL)
			require.NoError(t, err)

			// Track tunnel failures
			var startCount atomic.Int32
			failAttempts := atomic.Int32{}
			failAttempts.Store(tc.tunnelFailures)

			// Create port forwarder with test that can fail
			pf, err := newServicePortForwarder(t.Context(), func(_, _ string, localPort, _ int) portForward {
				return &testPortForward{
					localURL: &url.URL{
						Scheme: "http",
						Host:   fmt.Sprintf("127.0.0.1:%d", localPort),
					},
					serviceURL:   serviceURL,
					startCount:   &startCount,
					failAttempts: &failAttempts,
				}
			}, "test-ns", "test-sel", 0, servicePort)
			require.NoError(t, err)
			t.Cleanup(pf.Kill)

			resp, err := pf.Post(t.Context(), "/test", "body")

			switch {
			case tc.expectErrorContains != "":
				require.ErrorContains(t, err, tc.expectErrorContains)
			case tc.expectStaleErr:
				require.Error(t, err)
				require.True(t, isStaleConnectionError(err))
			default:
				require.NoError(t, err)
				require.Equal(t, tc.expectedResponse, string(resp))
			}

			require.Equal(t, tc.expectedStarts, startCount.Load())
		})
	}
}

func TestPortForwarder_ErrorCases(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		// Open a port and hold it to force start failure due to port conflict
		lc := net.ListenConfig{}
		lis, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = lis.Close()
		})
		localPort := lis.Addr().(*net.TCPAddr).Port

		dummyURL, err := url.Parse("http://localhost:9999")
		require.NoError(t, err)

		// This should fail because localPort is already in use
		_, err = newServicePortForwarder(t.Context(), func(_, _ string, lp, _ int) portForward {
			return &testPortForward{
				localURL: &url.URL{
					Scheme: "http",
					Host:   fmt.Sprintf("127.0.0.1:%d", lp),
				},
				serviceURL: dummyURL,
			}
		}, "test-ns", "test-sel", localPort, 0)
		require.Error(t, err)
		require.ErrorContains(t, err, "address already in use")
	})

	tests := []struct {
		name                string
		setupPortForwarder  func(*testing.T) PortForwarder
		ctx                 func(*testing.T) context.Context
		expectErrorContains string
	}{
		{
			name: "context canceled",
			setupPortForwarder: func(t *testing.T) PortForwarder {
				return newTestPortForwarder(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
				})
			},
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			expectErrorContains: "context canceled",
		},
		{
			name: "non ok status",
			setupPortForwarder: func(t *testing.T) PortForwarder {
				return newTestPortForwarder(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte("bad request"))
				})
			},
			ctx:                 func(t *testing.T) context.Context { return t.Context() },
			expectErrorContains: "request failed with status 400 Bad Request: bad request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pf := tc.setupPortForwarder(t)

			_, err := pf.Post(tc.ctx(t), "/test", "body")
			require.ErrorContains(t, err, tc.expectErrorContains)
		})
	}
}

func TestPortForwarder_Address(t *testing.T) {
	pf := newTestPortForwarder(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Parallel()

	addr := pf.Address()
	require.True(t, strings.HasPrefix(addr, "http://127.0.0.1:"))
}
