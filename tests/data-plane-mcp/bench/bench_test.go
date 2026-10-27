// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// 1. Build AIGW
//  	make clean build.aigw
// 2. Run the bench test
//   	go test -timeout=15m -run='^$' -bench=. ./tests/data-plane-mcp/bench/...

package bench

import (
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	internaltesting "github.com/envoyproxy/ai-gateway/internal/testing"
	"github.com/envoyproxy/ai-gateway/tests/internal/testmcp"
)

const (
	writeTimeout  = 120 * time.Second
	mcpServerPort = 8080
	aigwPort      = 1975
)

var aigwBinary = path.Join(internaltesting.FindProjectRoot(), fmt.Sprintf("/out/aigw-%s-%s", runtime.GOOS, runtime.GOARCH))

type MCPBenchCase struct {
	Name        string
	ProxyBinary string
	ProxyArgs   []string
	TestAddr    string
}

// setupBenchmark sets up the client connection.
func setupBenchmark(b *testing.B) []MCPBenchCase {
	b.Helper() // Treat this as a helper function

	// setup MCP server
	mcpServer, _ := testmcp.NewServer(&testmcp.Options{
		Port:              mcpServerPort,
		ForceJSONResponse: false,
		DumbEchoServer:    true,
		WriteTimeout:      writeTimeout,
		DisableLog:        true,
	})
	b.Cleanup(func() {
		_ = mcpServer.Close()
	})

	return []MCPBenchCase{
		{
			Name:     "Baseline_NoProxy",
			TestAddr: fmt.Sprintf("http://localhost:%d", mcpServerPort),
		},
		{
			Name:        "EAIGW_Default",
			TestAddr:    fmt.Sprintf("http://localhost:%d/mcp", aigwPort),
			ProxyBinary: aigwBinary,
			ProxyArgs:   []string{"run", "./aigw.yaml"},
		},
		{
			Name:        "EAIGW_Config_100",
			TestAddr:    fmt.Sprintf("http://localhost:%d/mcp", aigwPort),
			ProxyBinary: aigwBinary,
			ProxyArgs:   []string{"run", "./aigw.yaml", "--mcp-session-encryption-iterations=100"},
		},
		{
			Name:        "EAIGW_Inline_100",
			TestAddr:    fmt.Sprintf("http://localhost:%d/mcp", aigwPort),
			ProxyBinary: aigwBinary,
			ProxyArgs: []string{
				"run",
				"--mcp-session-encryption-iterations=100",
				`--mcp-json={"mcpServers":{"aigw":{"type":"http","url":"http://localhost:8080/mcp"}}}`,
			},
		},
	}
}

func BenchmarkMCP(b *testing.B) {
	cases := setupBenchmark(b)
	for _, tc := range cases {
		var proxy *exec.Cmd
		if tc.ProxyBinary != "" {
			proxy = startProxy(b, &tc)
		}

		b.Run(tc.Name, func(b *testing.B) {
			mcpClient := mcp.NewClient(&mcp.Implementation{Name: "bench-http-client", Version: "0.1.0"}, nil)
			var cs *mcp.ClientSession
			internaltesting.RequireEventuallyNoError(b, func() (err error) {
				cs, err = mcpClient.Connect(b.Context(), &mcp.StreamableClientTransport{Endpoint: tc.TestAddr}, nil)
				return err
			}, 10*time.Second, 500*time.Millisecond, "failed to connect to MCP server at %s", tc.TestAddr)
			// Close the session before the proxy is stopped, so its background stream
			// doesn't reconnect to the next case's proxy with a stale session ID.
			b.Cleanup(func() { _ = cs.Close() })

			tools, err := cs.ListTools(b.Context(), &mcp.ListToolsParams{})
			if err != nil {
				b.Fatalf("Failed to list tools: %v", err)
			}
			var toolName string
			for _, t := range tools.Tools {
				if strings.Contains(t.Name, "echo") {
					toolName = t.Name
					break
				}
			}
			if toolName == "" {
				b.Fatalf("no echo tool found")
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, err := cs.CallTool(b.Context(), &mcp.CallToolParams{
					Name:      toolName,
					Arguments: testmcp.ToolEchoArgs{Text: "hello MCP"},
				})
				if err != nil {
					b.Fatalf("MCP Tool call name %s failed at iteration %d: %v", toolName, i, err)
				}

				txt, ok := res.Content[0].(*mcp.TextContent)
				if !ok {
					b.Fatalf("unexpected content type")
				}
				if txt.Text != "dumb echo: hello MCP" {
					b.Fatalf("unexpected text: %q", txt.Text)
				}
			}
		})

		if proxy != nil {
			stopProxy(b, &tc, proxy)
		}
	}
}

// stopProxy kills the proxy process tree and waits until its listener port is released,
// so the next case doesn't connect to the previous proxy while it is still shutting down.
func stopProxy(b testing.TB, tc *MCPBenchCase, proxy *exec.Cmd) {
	if proxy.Process == nil {
		return
	}
	addr, err := url.Parse(tc.TestAddr)
	require.NoError(b, err)

	_ = syscall.Kill(-proxy.Process.Pid, syscall.SIGKILL)
	_ = proxy.Wait()

	// Envoy is a child of aigw in the same process group, and may hold the port briefly
	// after aigw itself has exited.
	require.Eventually(b, func() bool {
		conn, dialErr := (&net.Dialer{}).DialContext(b.Context(), "tcp", addr.Host)
		if dialErr != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 30*time.Second, 200*time.Millisecond, "proxy %s did not shut down in time", tc.Name)
}

func startProxy(b testing.TB, tc *MCPBenchCase) *exec.Cmd {
	addr, err := url.Parse(tc.TestAddr)
	require.NoError(b, err)

	cmd := exec.CommandContext(b.Context(), tc.ProxyBinary, tc.ProxyArgs...) // nolint: gosec
	cmd.Env = append(os.Environ(), pinnedEnvoyVersionEnv(b))
	// Keep proxy logs out of the timed request path, but dump them if setup or the
	// benchmark fails so startup issues are still diagnosable.
	logs := internaltesting.DumpLogsOnFail(b, tc.Name+" stdout", tc.Name+" stderr")
	cmd.Stdout = logs[0]
	cmd.Stderr = logs[1]
	// put into new process group so we can kill the entire process tree (and children)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(b, cmd.Start())

	// Wait until we can connect to the proxy. The timeout is generous to cover a cold
	// Envoy download on slow CI runners.
	require.Eventually(b, func() bool {
		conn, dialErr := (&net.Dialer{}).DialContext(b.Context(), "tcp", addr.Host)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Minute, 500*time.Millisecond, "proxy %s did not become ready in time", tc.Name)

	return cmd
}

func pinnedEnvoyVersionEnv(b testing.TB) string {
	b.Helper()

	// Pin the same Envoy version used by the other data-plane tests so the cached
	// binary is reused. `aigw run` consumes ENVOY_VERSION in both the file-backed
	// config path (`./aigw.yaml`) and the inline `--mcp-json` autoconfig path.
	envoyVersion, err := os.ReadFile(filepath.Join(internaltesting.FindProjectRoot(), ".envoy-version"))
	require.NoError(b, err)
	return "ENVOY_VERSION=" + strings.TrimSpace(string(envoyVersion))
}
