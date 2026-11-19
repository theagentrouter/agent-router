// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFetchReviews(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/org/repo/pulls/7/reviews", r.URL.Path)
		require.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		require.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/org/repo/pulls/7/reviews?per_page=100&page=2>; rel="next", <%s/repos/org/repo/pulls/7/reviews?per_page=100&page=2>; rel="last"`, srv.URL, srv.URL))
			_, _ = w.Write([]byte(`[{"user":{"login":"a"},"state":"APPROVED","body":"LGTM: ship it"},{"user":null,"state":"COMMENTED"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"user":{"login":"b"},"state":"CHANGES_REQUESTED","submitted_at":"2026-11-19T00:00:00Z"}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	reviews, err := fetchReviews(context.Background(), srv.Client(), srv.URL+"/", "org/repo", 7, "tok")
	require.NoError(t, err)
	require.Len(t, reviews, 3)
	require.Equal(t, "a", reviews[0].User.Login)
	require.Equal(t, "APPROVED", reviews[0].State)
	require.Nil(t, reviews[1].User)
	require.Equal(t, "b", reviews[2].User.Login)
	require.Equal(t, "CHANGES_REQUESTED", reviews[2].State)
}

func TestFetchReviewsErrors(t *testing.T) {
	t.Run("error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}))
		defer srv.Close()
		_, err := fetchReviews(context.Background(), srv.Client(), srv.URL, "org/repo", 1, "")
		require.ErrorContains(t, err, "403 Forbidden: rate limited")
	})
	t.Run("link to another host", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", `<https://example.com/steal>; rel="next"`)
			_, _ = w.Write([]byte(`[]`))
		}))
		defer srv.Close()
		_, err := fetchReviews(context.Background(), srv.Client(), srv.URL, "org/repo", 1, "tok")
		require.ErrorContains(t, err, "refusing to follow pagination link")
	})
	t.Run("endless pagination", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Link", fmt.Sprintf(`<%s/again>; rel="next"`, srv.URL))
			_, _ = w.Write([]byte(`[]`))
		}))
		defer srv.Close()
		_, err := fetchReviews(context.Background(), srv.Client(), srv.URL, "org/repo", 1, "")
		require.ErrorContains(t, err, "more than 50 pages")
	})
	t.Run("invalid body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"message": "not a list"}`))
		}))
		defer srv.Close()
		_, err := fetchReviews(context.Background(), srv.Client(), srv.URL, "org/repo", 1, "")
		require.ErrorContains(t, err, "decode reviews")
	})
}

func TestNextLink(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{header: "", want: ""},
		{header: `<https://api.github.com/x?page=2>; rel="next"`, want: "https://api.github.com/x?page=2"},
		{header: `<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`, want: "https://api.github.com/x?page=3"},
		{header: `<https://api.github.com/x?page=1>; rel="first"`, want: ""},
	} {
		require.Equal(t, tc.want, nextLink(tc.header), tc.header)
	}
}
