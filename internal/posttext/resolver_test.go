package posttext

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveText_Success(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.URL.Query().Get("uris"); got != "at://did:plc:bob/app.bsky.feed.post/abc" {
			t.Errorf("unexpected uris param: %q", got)
		}
		fmt.Fprintln(w, `{"posts":[{"uri":"at://did:plc:bob/app.bsky.feed.post/abc","record":{"text":"Hello world"}}]}`)
	}))
	defer srv.Close()

	r := NewResolver()
	r.SetAPIBaseURL(srv.URL)

	if got := r.ResolveText("at://did:plc:bob/app.bsky.feed.post/abc"); got != "Hello world" {
		t.Errorf("first call: got %q, want %q", got, "Hello world")
	}
	// Second call should hit cache.
	if got := r.ResolveText("at://did:plc:bob/app.bsky.feed.post/abc"); got != "Hello world" {
		t.Errorf("cached call: got %q, want %q", got, "Hello world")
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 HTTP call, got %d", calls.Load())
	}
}

func TestResolveText_EmptyURI(t *testing.T) {
	r := NewResolver()
	if got := r.ResolveText(""); got != "" {
		t.Errorf("empty URI should return empty, got %q", got)
	}
}

func TestResolveText_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Post not visible / deleted → empty posts array.
		fmt.Fprintln(w, `{"posts":[]}`)
	}))
	defer srv.Close()

	r := NewResolver()
	r.SetAPIBaseURL(srv.URL)

	if got := r.ResolveText("at://did:plc:bob/app.bsky.feed.post/gone"); got != "" {
		t.Errorf("missing post should resolve to empty, got %q", got)
	}
}

func TestResolveText_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewResolver()
	r.SetAPIBaseURL(srv.URL)

	if got := r.ResolveText("at://did:plc:bob/app.bsky.feed.post/x"); got != "" {
		t.Errorf("HTTP error should resolve to empty, got %q", got)
	}
}

func TestResolveText_NegativeCacheShortTTL(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintln(w, `{"posts":[]}`)
	}))
	defer srv.Close()

	r := NewResolver()
	r.SetAPIBaseURL(srv.URL)
	r.negativeTTL = 10 * time.Millisecond

	r.ResolveText("at://did:plc:bob/app.bsky.feed.post/x")
	r.ResolveText("at://did:plc:bob/app.bsky.feed.post/x") // cached
	if calls.Load() != 1 {
		t.Errorf("expected 1 call within neg TTL, got %d", calls.Load())
	}
	time.Sleep(20 * time.Millisecond)
	r.ResolveText("at://did:plc:bob/app.bsky.feed.post/x") // negative entry expired
	if calls.Load() != 2 {
		t.Errorf("expected refetch after neg TTL, got %d calls", calls.Load())
	}
}

func TestNewResolverWithCacheSize_FallbacksOnNonPositive(t *testing.T) {
	r := NewResolverWithCacheSize(0)
	if r.maxCacheSize != defaultMaxCacheSize {
		t.Errorf("0 should fall back to default, got %d", r.maxCacheSize)
	}
	r = NewResolverWithCacheSize(-5)
	if r.maxCacheSize != defaultMaxCacheSize {
		t.Errorf("negative should fall back to default, got %d", r.maxCacheSize)
	}
}

func TestSetAPIBaseURL_IgnoresEmpty(t *testing.T) {
	r := NewResolver()
	orig := r.apiBaseURL
	r.SetAPIBaseURL("")
	if r.apiBaseURL != orig {
		t.Errorf("empty URL should be ignored, got %q", r.apiBaseURL)
	}
}

func TestEvictOldest(t *testing.T) {
	r := NewResolverWithCacheSize(4)
	now := time.Now()
	for i := 0; i < 4; i++ {
		r.cache[fmt.Sprintf("uri%d", i)] = cacheEntry{
			text:     fmt.Sprintf("t%d", i),
			cachedAt: now.Add(time.Duration(i) * time.Second),
		}
	}
	r.evictOldest() // evicts 1 (25%)
	if _, ok := r.cache["uri0"]; ok {
		t.Errorf("oldest entry should be evicted")
	}
	if len(r.cache) != 3 {
		t.Errorf("expected 3 entries after eviction, got %d", len(r.cache))
	}
}
