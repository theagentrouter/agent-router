// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package testopenai

import (
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/envoyproxy/ai-gateway/internal/json"
)

type cassetteTestCase[R any] struct {
	cassette       Cassette
	request        *R
	expectedStatus int
}

func testNewRequest[R any](t *testing.T, tests []cassetteTestCase[R]) {
	// Use the real cassettes directory when writing as this test is the
	// documented way to backfill cassettes.
	server, err := NewServer(os.Stdout, 0)
	require.NoError(t, err)
	defer func() {
		// This sleep is required to wait until large cassettes are recorded.
		// Remove this sleep when there is a proper way to wait for cassettes to be recorded.
		<-time.After(5 * time.Second)
		server.Close()
	}()

	baseURL := server.URL()

	for _, tc := range tests {
		t.Run(tc.cassette.String(), func(t *testing.T) {
			// Create request using NewRequest which handles Azure transformation.
			req, err := NewRequest(t.Context(), baseURL, tc.cassette)
			require.NoError(t, err)

			// Verify the request is properly formed.
			require.Equal(t, http.MethodPost, req.Method)
			require.Equal(t, "application/json", req.Header.Get("Content-Type"))
			require.Equal(t, tc.cassette.String(), req.Header.Get(CassetteNameHeader))

			// Verify request body matches expected from requests map.
			body, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.NoError(t, req.Body.Close())

			expectedRequestBody, err := json.Marshal(tc.request)
			require.NoError(t, err, "could not marshal request body for cassette %s", tc.cassette)
			require.JSONEq(t, string(expectedRequestBody), string(body))

			// Actually send the request to verify it works with the fake server.
			req, err = NewRequest(t.Context(), baseURL, tc.cassette)
			require.NoError(t, err)

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			body, err = io.ReadAll(resp.Body)
			// For bad request cases, the server might send incorrect Content-Length
			// OpenAI once sent a Content-Length header 1 byte longer than its JSON.
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
				require.NoError(t, err)
			}

			// Verify the expected status code.
			require.Equal(t, tc.expectedStatus, resp.StatusCode,
				"Expected status %d for %s, got %d: %s",
				tc.expectedStatus, tc.cassette, resp.StatusCode, body)
		})
	}
}

func TestNewRequestError(t *testing.T) {
	type request struct {
		f float64
	}
	t.Run("unknown", func(t *testing.T) {
		_, err := NewRequest(t.Context(), "", Cassette(999))
		require.EqualError(t, err, "unknown cassette: unknown")
	})
	t.Run("nil context", func(t *testing.T) {
		r := request{1}
		_, err := newRequest(nil, Cassette(999), "/chat/completions", &r) //nolint:staticcheck
		require.EqualError(t, err, "net/http: nil Context")
	})
}

// testCassettes ensures all cassette constants have corresponding request bodies.
func testCassettes[R any](t *testing.T, cassettes []Cassette, requests map[Cassette]*R) {
	t.Helper()

	// Verify we have the same number of entries.
	require.Len(t, requests, len(cassettes), "Number of cassette should match number of requests")

	// Verify each cassette has a request.
	for _, cassette := range cassettes {
		request, ok := requests[cassette]
		require.True(t, ok, "Cassette %s should have a request", cassette)
		require.NotNil(t, request, "Request for cassette %s should not be nil", cassette)
	}
}

// buildTestCases returns a slice of all test cases in a consistent order.
func buildTestCases[R any](t *testing.T, requests map[Cassette]*R) ([]cassetteTestCase[R], error) {
	t.Helper()

	// sort so they are in a consistent order.
	result := make([]cassetteTestCase[R], 0, len(requests))
	for c := range _cassetteNameEnd {
		r, ok := requests[c]
		if !ok {
			continue // requests are a subset of all cassettes.
		}

		result = append(result, cassetteTestCase[R]{
			cassette:       c,
			request:        r,
			expectedStatus: http.StatusOK,
		})
	}
	require.Len(t, result, len(requests))
	return result, nil
}
