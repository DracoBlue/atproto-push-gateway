package profile

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
	cacheTTL       = 1 * time.Hour
	maxCacheSize   = 10000
	requestTimeout = 5 * time.Second
)

type cacheEntry struct {
	displayName string
	handle      string
	cachedAt    time.Time
}

type Resolver struct {
	mu     sync.RWMutex
	cache  map[string]cacheEntry
	client *http.Client
}

type profileResponse struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

func NewResolver() *Resolver {
	return &Resolver{
		cache: make(map[string]cacheEntry),
		client: &http.Client{
			Timeout: requestTimeout,
		},
	}
}

// ResolveProfile returns (displayName, handle) for a DID.
// Falls back to ("", "") on error — the client handles formatting.
func (r *Resolver) ResolveProfile(did string) (string, string) {
	// Check cache first
	r.mu.RLock()
	if entry, ok := r.cache[did]; ok && time.Since(entry.cachedAt) < cacheTTL {
		r.mu.RUnlock()
		return entry.displayName, entry.handle
	}
	r.mu.RUnlock()

	// Fetch profile from the public API
	displayName, handle := r.fetchProfile(did)

	// Cache the result
	r.mu.Lock()
	if len(r.cache) >= maxCacheSize {
		r.evictOldest()
	}
	r.cache[did] = cacheEntry{
		displayName: displayName,
		handle:      handle,
		cachedAt:    time.Now(),
	}
	r.mu.Unlock()

	return displayName, handle
}

// ResolveDisplayName returns a human-readable name for a DID.
// Returns displayName if available, then handle, then the DID.
func (r *Resolver) ResolveDisplayName(did string) string {
	displayName, handle := r.ResolveProfile(did)
	if displayName != "" {
		return displayName
	}
	if handle != "" {
		return handle
	}
	return did
}

func (r *Resolver) fetchProfile(did string) (string, string) {
	reqURL := fmt.Sprintf("https://public.api.bsky.app/xrpc/app.bsky.actor.getProfile?actor=%s", url.QueryEscape(did))

	resp, err := r.client.Get(reqURL)
	if err != nil {
		log.Printf("[profile] error fetching profile for %s: %v", did, err)
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[profile] non-200 response for %s: %d", did, resp.StatusCode)
		return "", ""
	}

	var profile profileResponse
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		log.Printf("[profile] error decoding profile for %s: %v", did, err)
		return "", ""
	}

	return profile.DisplayName, profile.Handle
}

// evictOldest removes the oldest quarter of cache entries.
// Must be called with r.mu held for writing.
func (r *Resolver) evictOldest() {
	type didTime struct {
		did      string
		cachedAt time.Time
	}

	entries := make([]didTime, 0, len(r.cache))
	for did, entry := range r.cache {
		entries = append(entries, didTime{did: did, cachedAt: entry.cachedAt})
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
		delete(r.cache, entries[oldestIdx].did)
		entries[oldestIdx] = entries[len(entries)-1]
		entries = entries[:len(entries)-1]
	}
}
