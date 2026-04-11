package profile

import (
	"testing"
	"time"
)

func TestResolveDisplayName_CacheHit(t *testing.T) {
	resolver := NewResolver()
	resolver.cache["did:plc:cached"] = cacheEntry{
		displayName: "Cached Name",
		cachedAt:    time.Now(),
	}

	name := resolver.ResolveDisplayName("did:plc:cached")
	if name != "Cached Name" {
		t.Errorf("expected 'Cached Name', got %q", name)
	}
}

func TestResolveDisplayName_CacheExpired(t *testing.T) {
	resolver := NewResolver()
	// Use a very short timeout client so the HTTP call fails fast
	resolver.client.Timeout = time.Millisecond

	resolver.cache["did:plc:expired"] = cacheEntry{
		displayName: "Old Name",
		cachedAt:    time.Now().Add(-2 * cacheTTL), // expired
	}

	// Should try to fetch (fail) and fall back to DID
	name := resolver.ResolveDisplayName("did:plc:expired")
	if name != "did:plc:expired" {
		t.Errorf("expected DID fallback for expired cache, got %q", name)
	}
}

func TestResolveDisplayName_FallbackToDID(t *testing.T) {
	resolver := NewResolver()
	resolver.client.Timeout = time.Millisecond

	name := resolver.ResolveDisplayName("did:plc:unknown")
	if name != "did:plc:unknown" {
		t.Errorf("expected DID fallback, got %q", name)
	}

	// Verify it was cached
	resolver.mu.RLock()
	_, ok := resolver.cache["did:plc:unknown"]
	resolver.mu.RUnlock()
	if !ok {
		t.Error("expected result to be cached")
	}
}

func TestEvictOldest(t *testing.T) {
	resolver := NewResolver()

	base := time.Now()
	for i := 0; i < 100; i++ {
		did := "did:plc:" + string(rune('A'+i/26)) + string(rune('a'+i%26))
		resolver.cache[did] = cacheEntry{
			displayName: did,
			cachedAt:    base.Add(time.Duration(i) * time.Second),
		}
	}

	if len(resolver.cache) != 100 {
		t.Fatalf("expected cache size 100, got %d", len(resolver.cache))
	}

	resolver.evictOldest()

	// Should evict 25 (100/4)
	if len(resolver.cache) != 75 {
		t.Errorf("expected cache size 75 after eviction, got %d", len(resolver.cache))
	}
}
