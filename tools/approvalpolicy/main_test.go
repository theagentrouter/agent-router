// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testOwnersYAML = `
maintainers:
  - {github: maint, name: Maintainer, company: Acme}
reviewers:
  - {github: rev, name: Reviewer, company: Globex}
`

func TestRun(t *testing.T) {
	dir := t.TempDir()
	ownersPath := filepath.Join(dir, "OWNERS")
	require.NoError(t, os.WriteFile(ownersPath, []byte(testOwnersYAML), 0o600))

	for _, tc := range []struct {
		name         string
		reviews      string
		wantApproved bool
		wantReport   string
	}{
		{
			name:         "approved",
			reviews:      `[{"user":{"login":"maint"},"state":"APPROVED"}]`,
			wantApproved: true,
			wantReport: "### Approval policy passed\n\nApproved by maintainer @maint.\n\n" +
				"See https://github.com/org/repo/blob/main/COMMUNITY_ROLES.md#how-pull-requests-get-approved\n",
		},
		{
			name:    "not approved",
			reviews: `[{"user":{"login":"rev"},"state":"APPROVED"}]`,
			wantReport: "### Approval policy not met yet\n\n" +
				"Needs an approval from 1 maintainer or from 2 reviewers. Reviewer approvals so far: @rev.\n\n" +
				"See https://github.com/org/repo/blob/main/COMMUNITY_ROLES.md#how-pull-requests-get-approved\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/repos/org/repo/pulls/42/reviews", r.URL.Path)
				_, _ = w.Write([]byte(tc.reviews))
			}))
			defer srv.Close()
			summaryPath := filepath.Join(t.TempDir(), "summary.md")
			env := map[string]string{
				"GITHUB_API_URL":      srv.URL,
				"GITHUB_STEP_SUMMARY": summaryPath,
			}

			var out bytes.Buffer
			approved, err := run(context.Background(),
				[]string{"-owners", ownersPath, "-repo", "org/repo", "-pr", "42", "-author", "outsider"},
				func(k string) string { return env[k] }, &out, srv.Client())
			require.NoError(t, err)
			require.Equal(t, tc.wantApproved, approved)
			require.Equal(t, tc.wantReport, out.String())

			summary, err := os.ReadFile(summaryPath)
			require.NoError(t, err)
			require.Equal(t, tc.wantReport, string(summary))
		})
	}
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()
	ownersPath := filepath.Join(dir, "OWNERS")
	require.NoError(t, os.WriteFile(ownersPath, []byte(testOwnersYAML), 0o600))
	badOwnersPath := filepath.Join(dir, "BAD_OWNERS")
	require.NoError(t, os.WriteFile(badOwnersPath, []byte("approvers: []\n"), 0o600))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	env := func(k string) string {
		if k == "GITHUB_API_URL" {
			return srv.URL
		}
		return ""
	}

	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing flags", args: []string{"-repo", "org/repo"}, wantErr: "-repo, -pr and -author are required"},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: "flag provided but not defined"},
		{name: "missing OWNERS", args: []string{"-owners", filepath.Join(dir, "missing"), "-repo", "org/repo", "-pr", "1", "-author", "a"}, wantErr: "no such file"},
		{name: "invalid OWNERS", args: []string{"-owners", badOwnersPath, "-repo", "org/repo", "-pr", "1", "-author", "a"}, wantErr: "unknown field"},
		{name: "API error", args: []string{"-owners", ownersPath, "-repo", "org/repo", "-pr", "1", "-author", "a"}, wantErr: "500 Internal Server Error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			approved, err := run(context.Background(), tc.args, env, &out, srv.Client())
			require.ErrorContains(t, err, tc.wantErr)
			require.False(t, approved)
		})
	}
}
