// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcpcache

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/envoyproxy/ai-gateway/internal/contextcache"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// This file supplies the GCP side of syncing: a contextcache.Source that walks
// cachedContents.list for one project and region. The loop, gate, and store writes live
// in contextcache.Syncer.

const (
	// listPageSize is the documented maximum for cachedContents.list; larger values are
	// coerced down by the API. Requesting it explicitly avoids an unspecified default.
	listPageSize = 1000

	// syncGatePrefix namespaces gate keys. Cache keys are 64 hex characters, so the
	// prefix cannot collide with them.
	syncGatePrefix = "gcpcache:sync:"
)

// cachedContentItem is the subset of a cachedContents list item the syncer uses.
type cachedContentItem struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	ExpireTime  string `json:"expireTime"` // RFC 3339
}

type listResponse struct {
	CachedContents []cachedContentItem `json:"cachedContents"`
	NextPageToken  string              `json:"nextPageToken"`
}

// newSyncer returns a syncer. With a nil gcpAuth the source reads the resolver's current
// credentials each round, so SetAuth takes effect on the next round; a non-nil gcpAuth
// pins the credentials (used by tests).
func (r *Resolver) newSyncer(gcpAuth filterapi.GCPAuthHandler, interval time.Duration) *contextcache.Syncer {
	return &contextcache.Syncer{
		Store:    r.store,
		Source:   &listSource{r: r, auth: gcpAuth},
		Interval: interval,
		Logger:   r.logger,
	}
}

// listSource is a contextcache.Source over cachedContents.list.
type listSource struct {
	r    *Resolver
	auth filterapi.GCPAuthHandler
	// pages is the number of pages read by the last List call, for tests.
	pages int
}

// creds returns the pinned credentials, or the resolver's current ones.
func (s *listSource) creds() filterapi.GCPAuthHandler {
	if s.auth != nil {
		return s.auth
	}
	return s.r.GetAuth()
}

// GateKey implements contextcache.Source.
func (s *listSource) GateKey() string {
	auth := s.creds()
	if auth == nil {
		return syncGatePrefix
	}
	return syncGatePrefix + auth.GCPProject() + "/" + auth.GCPRegion()
}

// List implements contextcache.Source. The API has no server-side filter, so the walk
// reads every entry in the project and region; only gateway-created ones are added.
func (s *listSource) List(ctx context.Context, add func(string, contextcache.Entry)) error {
	s.pages = 0
	auth := s.creds()
	if auth == nil {
		return nil
	}
	region, project := auth.GCPRegion(), auth.GCPProject()

	token, err := auth.GCPTokenSource().Token()
	if err != nil {
		return fmt.Errorf("get GCP access token: %w", err)
	}
	base, err := url.Parse(fmt.Sprintf(gcpCachedContentsBasePath, region, project, region))
	if err != nil {
		return fmt.Errorf("parse cachedContents URL: %w", err)
	}

	pageToken := ""
	for {
		lr, err := s.r.listPage(ctx, base, token.AccessToken, pageToken)
		if err != nil {
			return err
		}
		s.pages++
		for _, item := range lr.CachedContents {
			if key, e, ok := s.toEntry(item); ok {
				add(key, e)
			}
		}
		if lr.NextPageToken == "" {
			return nil
		}
		pageToken = lr.NextPageToken
	}
}

// toEntry converts a list item. It reports false for items the gateway did not create
// (their displayName is not a cache key) and for items whose expiry does not parse.
func (s *listSource) toEntry(item cachedContentItem) (string, contextcache.Entry, bool) {
	if !isCacheKey(item.DisplayName) || item.Name == "" {
		return "", contextcache.Entry{}, false
	}
	expireTime, err := time.Parse(time.RFC3339, item.ExpireTime)
	if err != nil {
		s.r.logger.Warn("gcpcache: skipping cachedContents entry with unparseable expireTime",
			slog.String("name", item.Name), slog.String("expireTime", item.ExpireTime))
		return "", contextcache.Entry{}, false
	}
	return item.DisplayName, contextcache.Entry{Name: item.Name, ExpireTime: expireTime}, true
}

// listPage fetches one page of cachedContents. It sets the query on a copy of base, so
// the caller's URL is not altered.
func (r *Resolver) listPage(ctx context.Context, base *url.URL, accessToken, pageToken string) (listResponse, error) {
	u := *base
	q := url.Values{}
	q.Set("pageSize", strconv.Itoa(listPageSize))
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return listResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return listResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return listResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return listResponse{}, fmt.Errorf("list cachedContents returned HTTP %d: %s", resp.StatusCode, body)
	}

	var lr listResponse
	if err = json.Unmarshal(body, &lr); err != nil {
		return listResponse{}, fmt.Errorf("failed to decode list response: %w", err)
	}
	return lr, nil
}

// isCacheKey reports whether s has the shape computeCacheKey produces: a hex-encoded
// SHA-256 digest.
func isCacheKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
