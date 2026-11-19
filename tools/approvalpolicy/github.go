// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"sigs.k8s.io/yaml"
)

// maxReviewPages bounds pagination at 100 reviews per page.
const maxReviewPages = 50

// fetchReviews returns every review on a pull request, oldest first.
func fetchReviews(ctx context.Context, client *http.Client, apiURL, repo string, pr int, token string) ([]review, error) {
	apiURL = strings.TrimSuffix(apiURL, "/")
	next := fmt.Sprintf("%s/repos/%s/pulls/%d/reviews?per_page=100", apiURL, repo, pr)
	var all []review
	for page := 0; next != ""; page++ {
		if page == maxReviewPages {
			return nil, fmt.Errorf("pull request has more than %d pages of reviews", maxReviewPages)
		}
		// The token is sent with every request, so only follow links back to the same API.
		if !strings.HasPrefix(next, apiURL+"/") {
			return nil, fmt.Errorf("refusing to follow pagination link outside %s: %s", apiURL, next)
		}
		reviews, link, err := fetchReviewPage(ctx, client, next, token)
		if err != nil {
			return nil, err
		}
		all = append(all, reviews...)
		next = nextLink(link)
	}
	return all, nil
}

func fetchReviewPage(ctx context.Context, client *http.Client, url, token string) ([]review, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: %s: %.200s", url, resp.Status, body)
	}
	var reviews []review
	// sigs.k8s.io/yaml also decodes JSON, which keeps this tool to one decoder.
	if err = yaml.Unmarshal(body, &reviews); err != nil {
		return nil, "", fmt.Errorf("decode reviews from %s: %w", url, err)
	}
	return reviews, resp.Header.Get("Link"), nil
}

// nextLink returns the rel="next" URL from a GitHub Link header, or "" if there is none.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		for _, f := range fields[1:] {
			if strings.TrimSpace(f) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(fields[0]), "<>")
			}
		}
	}
	return ""
}
