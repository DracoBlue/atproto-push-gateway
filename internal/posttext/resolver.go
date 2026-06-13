// Package posttext resolves the body text of an AT Protocol post by URI,
// against an AppView's app.bsky.feed.getPosts endpoint. Results are cached
// in an LRU. Negative results (post not found, deleted, fetch error) are
// also cached for a shorter window so we don't refetch on every like/repost.
package posttext

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	defaultAPIBaseURL    = "https://public.api.bsky.app"
	defaultCacheTTL      = 24 * time.Hour
	defaultNegativeTTL   = 5 * time.Minute
	defaultMaxCacheSize  = 10000
	defaultRequestTimeout = 5 * time.Second
)

type cacheEntry struct {
	text     string
	cachedAt time.Time
	negative bool
}

type Resolver struct {
	mu           sync.RWMutex
	cache        map[string]cacheEntry
	maxCacheSize int
	client       *http.Client
	apiBaseURL   string
	cacheTTL     time.Duration
	negativeTTL  time.Duration
}

type getPostsResponse struct {
	Posts []struct {
		URI    string `json:"uri"`
		Record struct {
			Text string `json:"text"`
		} `json:"record"`
	} `json:"posts"`
}

// NewResolver creates a resolver with default cache size.
func NewResolver() *Resolver {
	return NewResolverWithCacheSize(defaultMaxCacheSize)
}

// NewResolverWithCacheSize creates a resolver whose cache is capped at
// maxCacheSize entries. Sizes <= 0 fall back to the default.
func NewResolverWithCacheSize(maxCacheSize int) *Resolver {
	if maxCacheSize <= 0 {
		maxCacheSize = defaultMaxCacheSize
	}
	return &Resolver{
		cache:        make(map[string]cacheEntry),
		maxCacheSize: maxCacheSize,
		apiBaseURL:   defaultAPIBaseURL,
		cacheTTL:     defaultCacheTTL,
		negativeTTL:  defaultNegativeTTL,
		client: &http.Client{
			Timeout: defaultRequestTimeout,
		},
	}
}

// SetAPIBaseURL overrides the AppView base URL. Empty values are ignored.
func (r *Resolver) SetAPIBaseURL(u string) {
	if u != "" {
		r.apiBaseURL = u
	}
}

// ResolveText returns the post body text for an AT-URI, or "" on miss.
// A miss can mean: not found, deleted, fetch error, or timeout. The caller
// should treat "" as "send notification without post text".
func (r *Resolver) ResolveText(postURI string) string {
	if postURI == "" {
		return ""
	}

	r.mu.RLock()
	if entry, ok := r.cache[postURI]; ok {
		ttl := r.cacheTTL
		if entry.negative {
			ttl = r.negativeTTL
		}
		if time.Since(entry.cachedAt) < ttl {
			r.mu.RUnlock()
			return entry.text
		}
	}
	r.mu.RUnlock()

	text, ok := r.fetchPost(postURI)

	r.mu.Lock()
	if len(r.cache) >= r.maxCacheSize {
		r.evictOldest()
	}
	r.cache[postURI] = cacheEntry{
		text:     text,
		cachedAt: time.Now(),
		negative: !ok,
	}
	r.mu.Unlock()

	return text
}

func (r *Resolver) fetchPost(postURI string) (string, bool) {
	reqURL := fmt.Sprintf("%s/xrpc/app.bsky.feed.getPosts?uris=%s", r.apiBaseURL, url.QueryEscape(postURI))

	resp, err := r.client.Get(reqURL)
	if err != nil {
		log.Printf("[posttext] error fetching %s: %v", postURI, err)
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[posttext] non-200 response for %s: %d", postURI, resp.StatusCode)
		return "", false
	}

	var out getPostsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Printf("[posttext] error decoding response for %s: %v", postURI, err)
		return "", false
	}

	if len(out.Posts) == 0 {
		// Post not found / deleted / not visible.
		return "", false
	}

	return out.Posts[0].Record.Text, true
}

// evictOldest removes the oldest quarter of cache entries.
// Must be called with r.mu held for writing.
func (r *Resolver) evictOldest() {
	type uriTime struct {
		uri      string
		cachedAt time.Time
	}

	entries := make([]uriTime, 0, len(r.cache))
	for uri, entry := range r.cache {
		entries = append(entries, uriTime{uri: uri, cachedAt: entry.cachedAt})
	}

	toEvict := len(entries) / 4
	if toEvict < 1 {
		toEvict = 1
	}

	for i := 0; i < toEvict; i++ {
		oldestIdx := 0
		for j := 1; j < len(entries); j++ {
			if entries[j].cachedAt.Before(entries[oldestIdx].cachedAt) {
				oldestIdx = j
			}
		}
		delete(r.cache, entries[oldestIdx].uri)
		entries[oldestIdx] = entries[len(entries)-1]
		entries = entries[:len(entries)-1]
	}
}
