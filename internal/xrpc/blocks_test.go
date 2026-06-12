package xrpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestURIRKey(t *testing.T) {
	tests := []struct {
		uri  string
		want string
	}{
		{"at://did:plc:alice/app.bsky.graph.block/3kco5r7x", "3kco5r7x"},
		{"at://did:plc:alice/app.bsky.graph.block/", ""},
		{"no-slashes", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := uriRKey(tt.uri); got != tt.want {
			t.Errorf("uriRKey(%q) = %q, want %q", tt.uri, got, tt.want)
		}
	}
}

func TestBackfillBlocks_SeedsBlocksFromAppView(t *testing.T) {
	h, s := newTestHandler(t)

	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("repo"); got != "did:plc:alice" {
			t.Errorf("expected repo did:plc:alice, got %q", got)
		}
		if got := r.URL.Query().Get("collection"); got != "app.bsky.graph.block" {
			t.Errorf("expected block collection, got %q", got)
		}
		// Two pages: first with cursor, second without
		if r.URL.Query().Get("cursor") == "" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"records": []map[string]interface{}{
					{"uri": "at://did:plc:alice/app.bsky.graph.block/rkey1", "value": map[string]string{"subject": "did:plc:bob"}},
					{"uri": "at://did:plc:alice/app.bsky.graph.block/rkey2", "value": map[string]string{"subject": "did:plc:carol"}},
					{"uri": "at://did:plc:alice/app.bsky.graph.block/rkey3", "value": map[string]string{"subject": ""}}, // skipped
				},
				"cursor": "page2",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"records": []map[string]interface{}{
				{"uri": "at://did:plc:alice/app.bsky.graph.block/rkey4", "value": map[string]string{"subject": "did:plc:dave"}},
			},
		})
	}))
	defer srv.Close()

	orig := backfillEndpoint
	backfillEndpoint = srv.URL
	defer func() { backfillEndpoint = orig }()

	h.backfillBlocks("did:plc:alice")

	if requests != 2 {
		t.Errorf("expected 2 paginated requests, got %d", requests)
	}
	for _, blocked := range []string{"did:plc:bob", "did:plc:carol", "did:plc:dave"} {
		if !s.IsBlocked(blocked, "did:plc:alice") {
			t.Errorf("expected %s to be blocked after backfill", blocked)
		}
	}
	// The rkey index must work for backfilled blocks too
	removed, err := s.RemoveBlockByRKey("did:plc:alice", "rkey1")
	if err != nil {
		t.Fatal(err)
	}
	if removed != "did:plc:bob" {
		t.Errorf("expected rkey1 to map to did:plc:bob, got %q", removed)
	}
}

func TestBackfillBlocks_StopsOnHTTPError(t *testing.T) {
	h, s := newTestHandler(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	orig := backfillEndpoint
	backfillEndpoint = srv.URL
	defer func() { backfillEndpoint = orig }()

	h.backfillBlocks("did:plc:alice")

	_, blocks, _ := s.GetStats()
	if blocks != 0 {
		t.Errorf("expected no blocks after failed backfill, got %d", blocks)
	}
}
