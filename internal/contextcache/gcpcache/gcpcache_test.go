// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package gcpcache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/contextcache"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/internalapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
)

// -----------------------------------------------------------------------
// Fake GCPAuthHandler
// -----------------------------------------------------------------------

type fakeGCPAuth struct {
	token   string
	region  string
	project string
}

func (f *fakeGCPAuth) Do(_ context.Context, _ map[string]string, _ []byte) ([]internalapi.Header, error) {
	return nil, nil
}

func (f *fakeGCPAuth) GCPTokenSource() oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: f.token})
}
func (f *fakeGCPAuth) GCPRegion() string  { return f.region }
func (f *fakeGCPAuth) GCPProject() string { return f.project }

var _ filterapi.GCPAuthHandler = (*fakeGCPAuth)(nil)

// -----------------------------------------------------------------------
// Message builders
// -----------------------------------------------------------------------

func ephemeralFields() *openai.AnthropicContentFields {
	return &openai.AnthropicContentFields{
		CacheControl: anthropic.CacheControlEphemeralParam{
			Type: constant.ValueOf[constant.Ephemeral](),
		},
	}
}

func ephemeralFieldsWithTTL(ttl anthropic.CacheControlEphemeralTTL) *openai.AnthropicContentFields {
	return &openai.AnthropicContentFields{
		CacheControl: anthropic.CacheControlEphemeralParam{
			Type: constant.ValueOf[constant.Ephemeral](),
			TTL:  ttl,
		},
	}
}

func systemMsg(text string, fields *openai.AnthropicContentFields) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfSystem: &openai.ChatCompletionSystemMessageParam{
			Role: openai.ChatMessageRoleSystem,
			Content: openai.ContentUnion{Value: []openai.ChatCompletionContentPartTextParam{
				{Type: "text", Text: text, AnthropicContentFields: fields},
			}},
		},
	}
}

func userMsg(text string) openai.ChatCompletionMessageParamUnion {
	return openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Role:    openai.ChatMessageRoleUser,
			Content: openai.StringOrUserRoleContentUnion{Value: text},
		},
	}
}

// -----------------------------------------------------------------------
// Fake cachedContents server
// -----------------------------------------------------------------------

type fakeCacheServer struct {
	srv          *httptest.Server
	listResponse string
	createStatus int
	createBody   string
	// Counters are atomic because the httptest handler runs on a goroutine per
	// request, and the concurrency tests below drive many at once.
	listCalls   atomic.Int64
	createCalls atomic.Int64
}

func (f *fakeCacheServer) lists() int   { return int(f.listCalls.Load()) }
func (f *fakeCacheServer) creates() int { return int(f.createCalls.Load()) }

func newFakeCacheServer(t *testing.T, listBody, createBody string, createStatus int) *fakeCacheServer {
	t.Helper()
	f := &fakeCacheServer{
		listResponse: listBody,
		createStatus: createStatus,
		createBody:   createBody,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			f.listCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(f.listResponse))
		case http.MethodPost:
			f.createCalls.Add(1)
			w.WriteHeader(f.createStatus)
			_, _ = w.Write([]byte(f.createBody))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// redirectTransport rewrites all outbound requests to hit the fake server host.
type redirectTransport struct {
	fakeHost string
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req2 := req.Clone(req.Context())
	req2.URL.Scheme = "http"
	req2.URL.Host = t.fakeHost
	return http.DefaultTransport.RoundTrip(req2)
}

// resolverWithServer builds a resolver pointed at a fake Google server. An optional store
// may be supplied; with none, the no-op store applies and every lookup misses.
func resolverWithServer(srvURL string, store ...contextcache.Store) *Resolver {
	host := srvURL[len("http://"):]
	var s contextcache.Store
	if len(store) > 0 {
		s = store[0]
	}
	return New(&http.Client{
		Transport: &redirectTransport{fakeHost: host},
		Timeout:   5 * time.Second,
	}, s, syncAuth, nil)
}

// memStore is an in-process contextcache.Store for tests.
type memStore struct {
	mu      sync.Mutex
	entries map[string]contextcache.Entry
	// getErr, when set, is returned from every Get, standing in for an unreachable store.
	getErr error
	// setErr, when set, is returned from every Set.
	setErr error
	gets   atomic.Int64
	sets   atomic.Int64
}

func newMemStore() *memStore { return &memStore{entries: map[string]contextcache.Entry{}} }

func (m *memStore) Get(_ context.Context, key string) (contextcache.Entry, bool, error) {
	m.gets.Add(1)
	if m.getErr != nil {
		return contextcache.Entry{}, false, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e, ok, nil
}

func (m *memStore) Set(_ context.Context, key string, e contextcache.Entry, _ time.Duration) error {
	m.sets.Add(1)
	if m.setErr != nil {
		return m.setErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = e
	return nil
}

func (m *memStore) SetNX(_ context.Context, key string, e contextcache.Entry, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; ok {
		return false, nil
	}
	m.entries[key] = e
	return true, nil
}

// AcquireGate never wins: resolver tests using memStore do not exercise syncing.
func (m *memStore) AcquireGate(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (m *memStore) seed(key, cacheName string, expireTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = contextcache.Entry{Name: cacheName, ExpireTime: expireTime}
}

// computeKeyForRequest replicates the key generation for use in test assertions.
func computeKeyForRequest(t *testing.T, req *openai.ChatCompletionRequest) string {
	t.Helper()
	bp := findBreakpoint(req.Messages)
	require.GreaterOrEqual(t, bp, 0)
	contents, sys, err := computeKeyInputs(req.Model, req.Messages[:bp+1])
	require.NoError(t, err)
	key, err := computeCacheKey(syncAuth.project, syncAuth.region, req.Model, contents, sys, nil)
	require.NoError(t, err)
	return key
}

// -----------------------------------------------------------------------
// Unit tests: findBreakpoint
// -----------------------------------------------------------------------

func TestFindBreakpoint(t *testing.T) {
	tests := []struct {
		name     string
		messages []openai.ChatCompletionMessageParamUnion
		want     int
	}{
		{
			name:     "no markers returns -1",
			messages: []openai.ChatCompletionMessageParamUnion{userMsg("hello")},
			want:     -1,
		},
		{
			name: "system marker at index 0",
			messages: []openai.ChatCompletionMessageParamUnion{
				systemMsg("You are helpful.", ephemeralFields()),
				userMsg("hello"),
			},
			want: 0,
		},
		{
			name: "last marker wins when multiple exist",
			messages: []openai.ChatCompletionMessageParamUnion{
				systemMsg("sys", ephemeralFields()),
				userMsg("q1"),
				{OfUser: &openai.ChatCompletionUserMessageParam{
					Role: openai.ChatMessageRoleUser,
					Content: openai.StringOrUserRoleContentUnion{
						Value: []openai.ChatCompletionContentPartUserUnionParam{
							{OfText: &openai.ChatCompletionContentPartTextParam{
								Type:                   "text",
								Text:                   "q2",
								AnthropicContentFields: ephemeralFields(),
							}},
						},
					},
				}},
				userMsg("q3"),
			},
			want: 2,
		},
		{
			name: "tool message with cache_control",
			messages: []openai.ChatCompletionMessageParamUnion{
				{OfTool: &openai.ChatCompletionToolMessageParam{
					Role:                   openai.ChatMessageRoleTool,
					ToolCallID:             "call_1",
					Content:                openai.ContentUnion{Value: "result"},
					AnthropicContentFields: ephemeralFields(),
				}},
				userMsg("follow-up"),
			},
			want: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, findBreakpoint(tc.messages))
		})
	}
}

// -----------------------------------------------------------------------
// Unit tests: extractTTL / anthropicTTLToGCP
// -----------------------------------------------------------------------

func TestExtractTTL(t *testing.T) {
	tests := []struct {
		name    string
		msg     openai.ChatCompletionMessageParamUnion
		wantTTL string
	}{
		{
			name:    "no ttl → default 300s",
			msg:     systemMsg("sys", ephemeralFields()),
			wantTTL: "300s",
		},
		{
			name:    "5m → 300s",
			msg:     systemMsg("sys", ephemeralFieldsWithTTL("5m")),
			wantTTL: "300s",
		},
		{
			name:    "1h → 3600s",
			msg:     systemMsg("sys", ephemeralFieldsWithTTL("1h")),
			wantTTL: "3600s",
		},
		{
			name:    "raw GCP seconds passthrough",
			msg:     systemMsg("sys", ephemeralFieldsWithTTL("600s")),
			wantTTL: "600s",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantTTL, extractTTL(tc.msg))
		})
	}
}

// -----------------------------------------------------------------------
// Unit tests: computeCacheKey
// -----------------------------------------------------------------------

func TestComputeCacheKey_Deterministic(t *testing.T) {
	msgs := []openai.ChatCompletionMessageParamUnion{
		systemMsg("You are helpful.", ephemeralFields()),
	}
	contents, sys, err := computeKeyInputs("gemini-1.5-pro", msgs)
	require.NoError(t, err)

	k1, err := computeCacheKey("p", "r", "gemini-1.5-pro", contents, sys, nil)
	require.NoError(t, err)
	k2, err := computeCacheKey("p", "r", "gemini-1.5-pro", contents, sys, nil)
	require.NoError(t, err)
	assert.Equal(t, k1, k2)
	assert.Len(t, k1, 64, "expected SHA-256 hex digest")
}

func TestComputeCacheKey_DifferentModels_DifferentKeys(t *testing.T) {
	msgs := []openai.ChatCompletionMessageParamUnion{systemMsg("sys", ephemeralFields())}
	c, s, err := computeKeyInputs("gemini-1.5-pro", msgs)
	require.NoError(t, err)
	k1, _ := computeCacheKey("p", "r", "gemini-1.5-pro", c, s, nil)
	k2, _ := computeCacheKey("p", "r", "gemini-2.0-flash", c, s, nil)
	assert.NotEqual(t, k1, k2)
}

// A cache name is only usable in its own project and region, so the same prompt must
// key differently per location when backends share a Redis.
func TestComputeCacheKey_DifferentLocations_DifferentKeys(t *testing.T) {
	msgs := []openai.ChatCompletionMessageParamUnion{systemMsg("sys", ephemeralFields())}
	c, s, err := computeKeyInputs("gemini-1.5-pro", msgs)
	require.NoError(t, err)
	base, _ := computeCacheKey("p", "us-central1", "gemini-1.5-pro", c, s, nil)
	otherRegion, _ := computeCacheKey("p", "europe-west1", "gemini-1.5-pro", c, s, nil)
	otherProject, _ := computeCacheKey("q", "us-central1", "gemini-1.5-pro", c, s, nil)
	assert.NotEqual(t, base, otherRegion)
	assert.NotEqual(t, base, otherProject)
}

// -----------------------------------------------------------------------
// Integration tests: Resolve() against fake HTTP server
// -----------------------------------------------------------------------

func TestResolver_NoMarkers_ReturnsNil(t *testing.T) {
	// Credentials must be set, or Resolve returns nil before it looks at the markers.
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, "", http.StatusOK)
	r := resolverWithServer(fake.srv.URL)
	req := &openai.ChatCompletionRequest{
		Model:    "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{userMsg("hello")},
	}
	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, res, "no cache_control markers should return nil")
	assert.Equal(t, 0, fake.creates())
}

// A resolver built without credentials is inert: even a marked request is not cached.
func TestResolver_NilAuth_IsInert(t *testing.T) {
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, "", http.StatusOK)
	r := resolverWithServer(fake.srv.URL)
	r.SetAuth(nil)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}
	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	assert.Nil(t, res)
	assert.Equal(t, 0, fake.creates())
}

func TestResolver_CacheMiss_Creates(t *testing.T) {
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	createResp := `{"name":"projects/p/locations/us-central1/cachedContents/new","expireTime":"` + expireISO + `","usageMetadata":{"totalTokenCount":512}}`
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, createResp, http.StatusOK)
	r := resolverWithServer(fake.srv.URL)

	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("You are helpful.", ephemeralFields()),
			userMsg("Hello"),
		},
	}

	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/new", res.CacheName)
	assert.True(t, res.Created)
	assert.Equal(t, 512, res.TokenCount)
	// remainder = messages after the breakpoint (index 0 → only userMsg at index 1 remains)
	assert.Equal(t, []openai.ChatCompletionMessageParamUnion{userMsg("Hello")}, res.Messages)
	// The request path creates directly; it never lists.
	assert.Equal(t, 0, fake.lists())
	assert.Equal(t, 1, fake.creates())
}

// A cache that exists in Google but not in the store is not discovered on the request
// path: the resolver creates rather than scanning cachedContents. Repairing that drift is
// the syncer's job.
func TestResolver_RequestPath_NeverLists(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("You are helpful.", ephemeralFields()),
			userMsg("Hello"),
		},
	}
	key := computeKeyForRequest(t, req)
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)

	listBody, err := json.Marshal(map[string]interface{}{
		"cachedContents": []map[string]interface{}{
			{
				"name":        "projects/p/locations/us-central1/cachedContents/existing",
				"displayName": key,
				"model":       "publishers/google/models/gemini-1.5-pro",
				"expireTime":  expireISO,
			},
		},
	})
	require.NoError(t, err)

	createResp := `{"name":"projects/p/locations/us-central1/cachedContents/new","expireTime":"` + expireISO + `"}`
	fake := newFakeCacheServer(t, string(listBody), createResp, http.StatusOK)
	r := resolverWithServer(fake.srv.URL)

	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/new", res.CacheName)
	assert.True(t, res.Created)
	assert.Equal(t, 0, fake.lists(), "the request path must not list cachedContents")
	assert.Equal(t, 1, fake.creates())
}

func TestResolver_StoreHit_SkipsGoogleAPICalls(t *testing.T) {
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, "", http.StatusOK)
	store := newMemStore()
	r := resolverWithServer(fake.srv.URL, store)

	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("Cached system.", ephemeralFields()),
			userMsg("q"),
		},
	}
	key := computeKeyForRequest(t, req)
	store.seed(key, "projects/p/locations/r/cachedContents/store-hit", time.Now().Add(10*time.Minute))

	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/r/cachedContents/store-hit", res.CacheName)
	assert.False(t, res.Created)
	assert.Equal(t, 0, fake.lists(), "store hit must not call the Google API")
	assert.Equal(t, 0, fake.creates())
}

func TestResolver_StoreEntryExpiring_Refetches(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}
	key := computeKeyForRequest(t, req)
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)

	createResp := `{"name":"projects/p/locations/us-central1/cachedContents/refreshed","expireTime":"` + expireISO + `"}`
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`, createResp, http.StatusOK)
	store := newMemStore()
	r := resolverWithServer(fake.srv.URL, store)
	// Seed with an entry expiring inside the stale window → must be treated as a miss.
	store.seed(key, "projects/p/locations/us-central1/cachedContents/stale", time.Now().Add(5*time.Second))

	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/refreshed", res.CacheName)
	assert.Equal(t, 1, fake.creates(), "a near-expiry store entry must be treated as a miss")
	assert.Equal(t, 0, fake.lists())

	// The fresh entry replaces the stale one in the store.
	e, ok, err := store.Get(context.Background(), key)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/refreshed", e.Name)
}

// A create response without an expireTime cannot be stored (its TTL would be negative),
// so every later request would create again. It must fail instead of passing silently.
func TestResolver_CreateExpiryMissing_Fails(t *testing.T) {
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`,
		`{"name":"projects/p/locations/us-central1/cachedContents/x"}`, http.StatusOK)
	store := newMemStore()
	r := resolverWithServer(fake.srv.URL, store)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}

	res, err := r.Resolve(context.Background(), req)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "no expireTime")
}

// An unparseable expireTime fails JSON decoding of the create response.
func TestResolver_CreateExpiryUnparseable_Fails(t *testing.T) {
	fake := newFakeCacheServer(t, `{"cachedContents":[]}`,
		`{"name":"projects/p/locations/us-central1/cachedContents/x","expireTime":"not-a-time"}`, http.StatusOK)
	r := resolverWithServer(fake.srv.URL)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}

	res, err := r.Resolve(context.Background(), req)
	require.Error(t, err)
	assert.Nil(t, res)
}

func TestResolver_CreateFailure_ReturnsError(t *testing.T) {
	fake := newFakeCacheServer(t,
		`{"cachedContents":[]}`,
		`{"error":{"message":"below minimum tokens"}}`,
		http.StatusUnprocessableEntity,
	)
	r := resolverWithServer(fake.srv.URL)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}
	_, err := r.Resolve(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "422")
}

func TestResolver_TTLDefault_SentToGoogle(t *testing.T) {
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"cachedContents":[]}`))
			return
		}
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		capturedBody = buf[:n]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"projects/p/locations/r/cachedContents/x","expireTime":"` + expireISO + `"}`))
	}))
	defer srv.Close()

	r := resolverWithServer(srv.URL)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()), // no TTL → default 300s
			userMsg("q"),
		},
	}
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)

	var body gcp.CreateCachedContent
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, defaultTTL, body.TTL)
	// The resolver sets a TTL, not an expiry. A zero-value expiry must not be sent, or
	// Google would receive "0001-01-01T00:00:00Z" alongside the TTL.
	assert.Nil(t, body.ExpireTime)
	assert.NotContains(t, string(capturedBody), "expireTime")
}

func TestResolver_TTLOverride_SentToGoogle(t *testing.T) {
	expireISO := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var capturedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"cachedContents":[]}`))
			return
		}
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		capturedBody = buf[:n]
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"projects/p/locations/r/cachedContents/x","expireTime":"` + expireISO + `"}`))
	}))
	defer srv.Close()

	r := resolverWithServer(srv.URL)
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFieldsWithTTL("1h")),
			userMsg("q"),
		},
	}
	_, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)

	var body gcp.CreateCachedContent
	require.NoError(t, json.Unmarshal(capturedBody, &body))
	assert.Equal(t, "3600s", body.TTL)
}

// A replica keeps the cache it created, even if another replica created one for the same
// key. The list handler would return the other replica's cache on any call, so a
// reintroduced list (pre- or post-create) fails both the name and the list-count checks.
func TestResolver_DuplicateCreateRace_KeepsOwnCreate(t *testing.T) {
	req := &openai.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []openai.ChatCompletionMessageParamUnion{
			systemMsg("sys", ephemeralFields()),
			userMsg("q"),
		},
	}
	key := computeKeyForRequest(t, req)
	expireISO := time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)

	var listCalls atomic.Int64
	listBodyOther, _ := json.Marshal(map[string]interface{}{
		"cachedContents": []map[string]interface{}{
			{
				"name":        "projects/p/locations/us-central1/cachedContents/older-by-other",
				"displayName": key,
				"model":       "publishers/google/models/gemini-1.5-pro",
				"expireTime":  expireISO,
			},
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			listCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(listBodyOther)
		case http.MethodPost:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"projects/p/locations/us-central1/cachedContents/new-by-us","expireTime":"` + expireISO + `","usageMetadata":{"totalTokenCount":512}}`))
		}
	}))
	defer srv.Close()

	r := resolverWithServer(srv.URL)
	res, err := r.Resolve(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "projects/p/locations/us-central1/cachedContents/new-by-us", res.CacheName)
	// The replica performed and paid for the create, so it must report it.
	assert.True(t, res.Created)
	assert.Equal(t, 512, res.TokenCount)
	assert.Equal(t, int64(0), listCalls.Load(), "the request path must not list cachedContents")
}
