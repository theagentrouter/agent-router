// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Command approvalpolicy checks that a pull request has the approvals COMMUNITY_ROLES.md
// requires: one from a maintainer, or two from reviewers where at least one works at a
// different company from the author.
//
// .github/workflows/approval_policy.yaml runs it from the main branch, so a pull request can't
// change the rules or the OWNERS roster that it is checked against.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	client := &http.Client{Timeout: 30 * time.Second}
	approved, err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, client)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "approval policy: %v\n", err)
		os.Exit(1)
	}
	if !approved {
		os.Exit(1)
	}
}

// run checks the pull request named in args and reports whether it has the approvals it needs.
// It writes a short report to out and, when GITHUB_STEP_SUMMARY is set, to the job summary.
func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer, client *http.Client) (bool, error) {
	fs := flag.NewFlagSet("approvalpolicy", flag.ContinueOnError)
	ownersPath := fs.String("owners", "OWNERS", "path to the OWNERS file")
	repo := fs.String("repo", "", "repository, as owner/name")
	pr := fs.Int("pr", 0, "pull request number")
	author := fs.String("author", "", "GitHub login of the pull request author")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if *repo == "" || *pr <= 0 || *author == "" {
		return false, errors.New("-repo, -pr and -author are required")
	}

	data, err := os.ReadFile(*ownersPath)
	if err != nil {
		return false, err
	}
	o, err := parseOwners(data)
	if err != nil {
		return false, err
	}

	apiURL := getenv("GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	reviews, err := fetchReviews(ctx, client, apiURL, *repo, *pr, getenv("GITHUB_TOKEN"))
	if err != nil {
		return false, err
	}

	d := evaluate(o, *author, reviews)
	report := formatReport(d, *repo)
	if _, err = io.WriteString(out, report); err != nil {
		return false, err
	}
	if path := getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err = appendFile(path, report); err != nil {
			return false, err
		}
	}
	return d.approved, nil
}

func formatReport(d decision, repo string) string {
	status := "passed"
	if !d.approved {
		status = "not met yet"
	}
	return fmt.Sprintf("### Approval policy %s\n\n%s\n\nSee https://github.com/%s/blob/main/COMMUNITY_ROLES.md#how-pull-requests-get-approved\n",
		status, d.message, repo)
}

func appendFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(s); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
