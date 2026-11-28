// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package mcpproxy

import (
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestListResultDeterministicSchemas(t *testing.T) {
	id, err := jsonrpc.MakeID("list")
	require.NoError(t, err)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"z": map[string]any{"type": "string", "description": "<value>"},
			"a": map[string]any{"type": "integer", "minimum": 0},
		},
	}
	result := mcp.ListToolsResult{Tools: []*mcp.Tool{{Name: "test", InputSchema: schema, OutputSchema: schema}}}
	const wantSchema = `{"properties":{"a":{"minimum":0,"type":"integer"},"z":{"description":"<value>","type":"string"}},"type":"object"}`
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "modern"}[modern], func(t *testing.T) {
			var previous string
			for range 32 {
				rr := httptest.NewRecorder()
				if modern {
					writeJSONRPCResult(rr, id, &result)
				} else {
					events := make(chan *backendEvent)
					close(events)
					err := sendToAllBackendsAndAggregateResponsesImpl(t.Context(), events, newTestMCPProxy(), rr, &session{},
						&jsonrpc.Request{ID: id, Method: "tools/list"}, &mcp.ListToolsParams{},
						func(*session, []broadCastResponse[mcp.ListToolsResult]) mcp.ListToolsResult { return result }, nil)
					require.NoError(t, err)
				}
				body := rr.Body.String()
				require.Contains(t, body, `"inputSchema":`+wantSchema)
				require.Contains(t, body, `"outputSchema":`+wantSchema)
				if modern {
					require.Contains(t, body, `"resultType":"complete"`)
					if previous != "" {
						require.Equal(t, previous, body)
					}
					previous = body
				}
			}
		})
	}
}
