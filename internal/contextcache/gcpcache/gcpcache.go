// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

// Package gcpcache implements context-cache resolution for GCP Vertex AI (Gemini).
//
// When a client sends an OpenAI-format request with Anthropic-style cache_control markers,
// the resolver:
//  1. Finds the last cache_control breakpoint in the message list.
//  2. Splits the request into a cached prefix (tools + system + messages up to the breakpoint)
//     and a non-cached remainder.
//  3. Generates a deterministic SHA-256 cache key and looks it up in the shared store.
//  4. On a store miss, creates the cache entry, publishes it to the store, and returns
//     the cache resource name and the non-cached messages.
package gcpcache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/envoyproxy/ai-gateway/internal/apischema/gcp"
	"github.com/envoyproxy/ai-gateway/internal/apischema/openai"
	"github.com/envoyproxy/ai-gateway/internal/contextcache"
	"github.com/envoyproxy/ai-gateway/internal/filterapi"
	"github.com/envoyproxy/ai-gateway/internal/json"
	"github.com/envoyproxy/ai-gateway/internal/translator"
)

const (
	// defaultTTL is the default cache TTL when none is specified in the cache_control marker.
	defaultTTL = "300s"

	// gcpCachedContentsBasePath is the base URL for the Vertex AI cachedContents REST API.
	gcpCachedContentsBasePath = "https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/cachedContents"
)

// Resolver is the GCP Vertex AI contextcache.CacheResolver and contextcache.CacheSyncer.
type Resolver struct {
	httpClient *http.Client

	// store holds resolved cache names. Backed by Redis it is shared across replicas, so
	// a name published by one is reused by all. It does not prevent simultaneous creates
	// on a cold prefix. Its failures are never fatal: they are logged and treated as a
	// miss.
	store contextcache.Store

	// logger records store failures, which are otherwise invisible because the request
	// proceeds normally.
	logger *slog.Logger

	// mu guards auth and the background syncer's lifecycle.
	mu sync.Mutex
	// auth is the backend's current credentials. Resolve uses them to create caches and
	// the background syncer uses them to list them. Nil makes the resolver inert. It is
	// replaced by SetAuth on every config reload, so rotated tokens and changed
	// project/region take effect without rebuilding the resolver.
	auth filterapi.GCPAuthHandler
	// cancel stops the syncer; nil until Start runs one.
	cancel context.CancelFunc
	// done is closed when the syncer goroutine exits.
	done chan struct{}
	// closed is set by Close, after which Start does nothing.
	closed bool
}

// syncInterval is how often the background syncer runs a round. Between rounds,
// a cache the store has forgotten is not rediscovered, so this bounds how long drift
// persists. It is sized against the 300s default cache TTL.
const syncInterval = 60 * time.Second

var _ contextcache.CacheResolver = (*Resolver)(nil)

var _ contextcache.CacheSyncer = (*Resolver)(nil)

// Start implements contextcache.CacheSyncer. It does nothing if the resolver was built
// without credentials.
func (r *Resolver) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.cancel != nil || r.auth == nil {
		return
	}
	// A nil auth makes the syncer read the resolver's current credentials each round.
	rc := r.newSyncer(nil, syncInterval)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})
	go func() {
		defer close(r.done)
		rc.Run(ctx)
	}()
}

// SetAuth replaces the credentials used by Resolve and the background syncer. LoadConfig
// calls it on every reload, because the auth handler is rebuilt each time (the access
// token is rotated by the controller) while the resolver is reused.
func (r *Resolver) SetAuth(auth filterapi.GCPAuthHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth = auth
}

// GetAuth returns the credentials in effect. It takes the lock because SetAuth may run
// concurrently from a config reload.
func (r *Resolver) GetAuth() filterapi.GCPAuthHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth
}

// Close implements contextcache.CacheSyncer.
func (r *Resolver) Close() error {
	r.mu.Lock()
	r.closed = true
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

// New creates a new Resolver.
//
// httpClient is used for calls to the GCP cachedContents API; pass nil for a default.
// store holds resolved cache names; pass nil to disable caching entirely, which makes
// resolution inert rather than failing requests. auth is the backend's credentials, used
// for both resolving and syncing; nil makes the resolver inert. logger may be nil.
func New(httpClient *http.Client, store contextcache.Store, auth filterapi.GCPAuthHandler, logger *slog.Logger) *Resolver {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if store == nil {
		store = contextcache.NoopStore{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Resolver{httpClient: httpClient, store: store, logger: logger, auth: auth}
}

// Resolve implements contextcache.CacheResolver.
func (r *Resolver) Resolve(ctx context.Context, openAIReq *openai.ChatCompletionRequest) (*contextcache.ResolveResult, error) {
	// Without credentials nothing can be created, so caching is inert.
	auth := r.GetAuth()
	if auth == nil {
		return nil, nil
	}
	// Find the last cache_control breakpoint in the message list.
	breakpoint := findBreakpoint(openAIReq.Messages)
	if breakpoint < 0 {
		// No markers — nothing to cache.
		return nil, nil
	}

	// Split cached prefix.
	cachedMessages := openAIReq.Messages[:breakpoint+1]
	remainderMessages := openAIReq.Messages[breakpoint+1:]

	// Extract the TTL from the breakpoint message. If not found, fall back to default.
	ttl := extractTTL(openAIReq.Messages[breakpoint])

	// Build the Gemini cached prefix (tools + system + contents).
	contents, systemInstruction, err := translator.OpenAIMessagesToGeminiContents(cachedMessages, openAIReq.Model)
	if err != nil {
		return nil, fmt.Errorf("gcpcache: failed to convert cached messages to Gemini format: %w", err)
	}
	geminiTools, err := translator.OpenAIToolsToGeminiTools(openAIReq.Tools, false)
	if err != nil {
		return nil, fmt.Errorf("gcpcache: failed to convert tools to Gemini format: %w", err)
	}

	// Compute a deterministic cache key.
	cacheKey, err := computeCacheKey(auth.GCPProject(), auth.GCPRegion(), openAIReq.Model, contents, systemInstruction, geminiTools)
	if err != nil {
		return nil, fmt.Errorf("gcpcache: failed to compute cache key: %w", err)
	}

	// Check the shared store first. A store failure is not fatal: it is logged and
	// treated as a miss, so an unreachable store degrades caching rather than the
	// request. GCP-side failures below are a different matter and do fail fast.
	if e, ok := r.storeGet(ctx, cacheKey); ok {
		return &contextcache.ResolveResult{
			CacheName:  e.Name,
			Messages:   remainderMessages,
			ExpireTime: e.ExpireTime,
		}, nil
	}

	// Store miss — create against GCP. See resolveUncached for how concurrent
	// resolutions of the same key behave.
	result, err := r.resolveUncached(ctx, openAIReq, auth, cacheKey, ttl, contents, systemInstruction, geminiTools)
	if err != nil {
		return nil, err
	}

	// resolveUncached leaves Messages unset because it deals only in the cached prefix;
	// the remainder is per-request.
	result.Messages = remainderMessages
	return result, nil
}

// resolveUncached creates a GCP cachedContents entry for a cache key that missed the
// store, and publishes it. It deals only in the cached prefix, so it leaves
// ResolveResult.Messages unset for the caller to fill in per-request.
//
// It neither waits on other replicas nor lists existing caches. Replicas that miss the
// same cold prefix at the same moment each create; each keeps and bills its own cache
// (Created is true, so the write shows up in cache-write token metrics), and the surplus
// expires with its TTL.
func (r *Resolver) resolveUncached(
	ctx context.Context,
	openAIReq *openai.ChatCompletionRequest,
	gcpAuth filterapi.GCPAuthHandler,
	cacheKey, ttl string,
	contents []genai.Content,
	systemInstruction *genai.Content,
	geminiTools []genai.Tool,
) (*contextcache.ResolveResult, error) {
	region := gcpAuth.GCPRegion()
	project := gcpAuth.GCPProject()

	tokenSrc := gcpAuth.GCPTokenSource()
	token, err := tokenSrc.Token()
	if err != nil {
		return nil, fmt.Errorf("gcpcache: failed to get GCP access token: %w", err)
	}
	accessToken := token.AccessToken

	// Create without waiting on other replicas. A replica racing on the same cold prefix
	// may create too; each keeps the cache it created, and the store holds whichever name
	// was published last. Both are valid GCP caches.
	baseURL := fmt.Sprintf(gcpCachedContentsBasePath, region, project, region)
	created, tokenCount, expireTime, err := r.createCache(ctx, baseURL, accessToken, openAIReq.Model, region, project, cacheKey, contents, systemInstruction, geminiTools, ttl)
	if err != nil {
		return nil, fmt.Errorf("gcpcache: failed to create cached content: %w", err)
	}

	r.storeSet(ctx, cacheKey, contextcache.Entry{Name: created, ExpireTime: expireTime})
	return &contextcache.ResolveResult{
		CacheName:  created,
		Created:    true,
		TokenCount: tokenCount,
		ExpireTime: expireTime,
	}, nil
}

// -----------------------------------------------------------------------
// Store access
//
// The resolver never propagates a store failure to its caller: caching is an
// optimization, and an unreachable store must not turn a servable request into an
// error. Failures are logged and treated as a miss, leaving GCP as the source of
// truth. GCP-side failures are propagated, because those are actionable by the
// user — a create rejected for being below the model's minimum token count means the
// cache_control markers are misplaced, and silently serving the request uncached
// would hide a cost increase.
// -----------------------------------------------------------------------

// storeGet reads a resolved cache name, reporting a miss on any failure.
func (r *Resolver) storeGet(ctx context.Context, key string) (contextcache.Entry, bool) {
	e, ok, err := r.store.Get(ctx, key)
	if err != nil {
		r.logger.Warn("gcpcache: cache store read failed, proceeding uncached",
			slog.String("error", err.Error()))
		return contextcache.Entry{}, false
	}
	if !ok {
		return contextcache.Entry{}, false
	}
	// Treat entries expiring within 10s as stale so there is time to re-resolve before
	// the cache disappears underneath an in-flight request.
	if time.Until(e.ExpireTime) < contextcache.StaleThreshold {
		return contextcache.Entry{}, false
	}
	return e, true
}

// storeSet records a resolved cache name, expiring it with the GCP entry itself so
// the store cannot outlive what it points at.
func (r *Resolver) storeSet(ctx context.Context, key string, e contextcache.Entry) {
	ttl := time.Until(e.ExpireTime)
	if ttl <= 0 {
		return
	}
	if err := r.store.Set(ctx, key, e, ttl); err != nil {
		r.logger.Warn("gcpcache: cache store write failed",
			slog.String("error", err.Error()))
	}
}

// -----------------------------------------------------------------------
// Breakpoint and TTL helpers
// -----------------------------------------------------------------------

// findBreakpoint returns the index of the last message that contains a
// cache_control marker, or -1 if none is found.
func findBreakpoint(messages []openai.ChatCompletionMessageParamUnion) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messageHasCacheControl(&messages[i]) {
			return i
		}
	}
	return -1
}

// messageHasCacheControl returns true if the message contains at least one
// content part with an Anthropic-style cache_control marker.
func messageHasCacheControl(msg *openai.ChatCompletionMessageParamUnion) bool {
	if msg.OfTool != nil && isCacheControlSet(msg.OfTool.AnthropicContentFields) {
		return true
	}
	if msg.OfSystem != nil {
		if parts, ok := msg.OfSystem.Content.Value.([]openai.ChatCompletionContentPartTextParam); ok {
			for i := range parts {
				if isCacheControlSet(parts[i].AnthropicContentFields) {
					return true
				}
			}
		}
	}
	if msg.OfUser != nil {
		if parts, ok := msg.OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam); ok {
			for i := range parts {
				p := &parts[i]
				if p.OfText != nil && isCacheControlSet(p.OfText.AnthropicContentFields) {
					return true
				}
				if p.OfImageURL != nil && isCacheControlSet(p.OfImageURL.AnthropicContentFields) {
					return true
				}
				if p.OfInputAudio != nil && isCacheControlSet(p.OfInputAudio.AnthropicContentFields) {
					return true
				}
			}
		}
	}
	return false
}

// isCacheControlSet returns true if the AnthropicContentFields carries an ephemeral cache marker.
func isCacheControlSet(fields *openai.AnthropicContentFields) bool {
	return fields != nil && string(fields.CacheControl.Type) == "ephemeral"
}

// extractTTL returns the GCP TTL string (e.g. "3600s") from the last cache_control
// marker found in the message. Falls back to defaultTTL when none is specified.
// Anthropic TTL values ("5m", "1h") are converted to GCP seconds format.
func extractTTL(msg openai.ChatCompletionMessageParamUnion) string {
	var ttl string
	checkFields := func(f *openai.AnthropicContentFields) {
		if f != nil && string(f.CacheControl.Type) == "ephemeral" && string(f.CacheControl.TTL) != "" {
			ttl = anthropicTTLToGCP(string(f.CacheControl.TTL))
		}
	}
	if msg.OfTool != nil {
		checkFields(msg.OfTool.AnthropicContentFields)
	}
	if msg.OfSystem != nil {
		if parts, ok := msg.OfSystem.Content.Value.([]openai.ChatCompletionContentPartTextParam); ok {
			for i := range parts {
				checkFields(parts[i].AnthropicContentFields)
			}
		}
	}
	if msg.OfUser != nil {
		if parts, ok := msg.OfUser.Content.Value.([]openai.ChatCompletionContentPartUserUnionParam); ok {
			for i := range parts {
				p := &parts[i]
				if p.OfText != nil {
					checkFields(p.OfText.AnthropicContentFields)
				}
				if p.OfImageURL != nil {
					checkFields(p.OfImageURL.AnthropicContentFields)
				}
				if p.OfInputAudio != nil {
					checkFields(p.OfInputAudio.AnthropicContentFields)
				}
			}
		}
	}
	if ttl == "" {
		return defaultTTL
	}
	return ttl
}

// anthropicTTLToGCP converts Anthropic cache TTL values to GCP seconds format.
// Anthropic supports "5m" and "1h"; unknown values fall back to defaultTTL.
func anthropicTTLToGCP(ttl string) string {
	switch ttl {
	case "5m":
		return "300s"
	case "1h":
		return "3600s"
	default:
		// If the value is already in GCP seconds format (e.g. "600s"), pass it through.
		if len(ttl) > 1 && ttl[len(ttl)-1] == 's' {
			return ttl
		}
		return defaultTTL
	}
}

// computeKeyInputs converts a cached message prefix into Gemini format for key generation.
func computeKeyInputs(model string, cachedMessages []openai.ChatCompletionMessageParamUnion) ([]genai.Content, *genai.Content, error) {
	contents, sys, err := translator.OpenAIMessagesToGeminiContents(cachedMessages, model)
	if err != nil {
		return nil, nil, err
	}
	return contents, sys, nil
}

// -----------------------------------------------------------------------
// Cache key
// -----------------------------------------------------------------------

type cacheKeyInput struct {
	// Project and Region scope the key to where the cache lives: a cachedContents name is
	// only usable in its own project and region, and backends in different locations may
	// share one Redis.
	Project           string          `json:"project"`
	Region            string          `json:"region"`
	Model             string          `json:"model"`
	Contents          []genai.Content `json:"contents"`
	SystemInstruction *genai.Content  `json:"systemInstruction,omitempty"`
	Tools             []genai.Tool    `json:"tools,omitempty"`
}

// computeCacheKey generates a deterministic SHA-256 hex digest over the
// (project, region, model, contents, systemInstruction, tools) tuple. The digest is used
// as the GCP cachedContents displayName.
func computeCacheKey(project, region, model string, contents []genai.Content, systemInstruction *genai.Content, tools []genai.Tool) (string, error) {
	input := cacheKeyInput{
		Project:           project,
		Region:            region,
		Model:             model,
		Contents:          contents,
		SystemInstruction: systemInstruction,
		Tools:             tools,
	}
	b, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("failed to marshal cache key input: %w", err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// -----------------------------------------------------------------------
// GCP cachedContents REST API calls
// -----------------------------------------------------------------------

// createCache creates a cached content in GCP and returns the new cache's resource name,
// token count, and expiry time.
func (r *Resolver) createCache(
	ctx context.Context,
	baseURL, accessToken, model, region, project, cacheKey string,
	contents []genai.Content,
	systemInstruction *genai.Content,
	tools []genai.Tool,
	ttl string,
) (name string, tokenCount int, expireTime time.Time, err error) {
	// Vertex AI expects the full model resource name.
	fullModel := fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s", project, region, model)

	body := gcp.CreateCachedContent{
		Model:             fullModel,
		Contents:          contents,
		SystemInstruction: systemInstruction,
		Tools:             tools,
		DisplayName:       cacheKey,
		TTL:               ttl,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", 0, time.Time{}, fmt.Errorf("failed to marshal create cache request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", 0, time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, time.Time{}, fmt.Errorf("create cachedContent returned HTTP %d: %s", resp.StatusCode, respBody)
	}

	var cr gcp.CachedContent
	if err = json.Unmarshal(respBody, &cr); err != nil {
		return "", 0, time.Time{}, fmt.Errorf("failed to decode create cache response: %w", err)
	}
	// A zero expiry would make storeSet skip the write, so every later request for this
	// prefix would create again with nothing logged. Fail visibly instead.
	if cr.ExpireTime.IsZero() {
		return "", 0, time.Time{}, fmt.Errorf("create cachedContent response has no expireTime: %s", respBody)
	}

	if cr.UsageMetadata != nil {
		tokenCount = int(cr.UsageMetadata.TotalTokenCount)
	}
	return cr.Name, tokenCount, cr.ExpireTime, nil
}
