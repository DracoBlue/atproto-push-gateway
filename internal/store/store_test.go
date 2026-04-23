package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRegisterToken(t *testing.T) {
	s := newTestStore(t)

	if err := s.RegisterToken("did:plc:alice", "ios", "token123", "app.test"); err != nil {
		t.Fatalf("RegisterToken failed: %v", err)
	}

	if !s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be registered")
	}

	if s.IsRegistered("did:plc:bob") {
		t.Error("expected did:plc:bob to not be registered")
	}
}

func TestUnregisterToken(t *testing.T) {
	s := newTestStore(t)

	s.RegisterToken("did:plc:alice", "ios", "token123", "app.test")
	s.UnregisterToken("did:plc:alice", "ios", "token123", "app.test")

	if s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be unregistered after removing only token")
	}
}

func TestUnregisterOneOfMultipleTokens(t *testing.T) {
	s := newTestStore(t)

	s.RegisterToken("did:plc:alice", "ios", "token1", "app.test")
	s.RegisterToken("did:plc:alice", "android", "token2", "app.test")
	s.UnregisterToken("did:plc:alice", "ios", "token1", "app.test")

	if !s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to still be registered (has second token)")
	}
}

func TestGetTokensForDID(t *testing.T) {
	s := newTestStore(t)

	s.RegisterToken("did:plc:alice", "ios", "token1", "app.test")
	s.RegisterToken("did:plc:alice", "android", "token2", "app.test")
	s.RegisterToken("did:plc:bob", "ios", "token3", "app.other")

	tokens, err := s.GetTokensForDID("did:plc:alice")
	if err != nil {
		t.Fatalf("GetTokensForDID failed: %v", err)
	}
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens for alice, got %d", len(tokens))
	}

	tokens, err = s.GetTokensForDID("did:plc:bob")
	if err != nil {
		t.Fatalf("GetTokensForDID failed: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("expected 1 token for bob, got %d", len(tokens))
	}

	tokens, err = s.GetTokensForDID("did:plc:unknown")
	if err != nil {
		t.Fatalf("GetTokensForDID failed: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens for unknown, got %d", len(tokens))
	}
}

func TestUpsertToken(t *testing.T) {
	s := newTestStore(t)

	s.RegisterToken("did:plc:alice", "ios", "token1", "app.v1")
	s.RegisterToken("did:plc:alice", "ios", "token1", "app.v2") // same DID+token, different appId

	tokens, _ := s.GetTokensForDID("did:plc:alice")
	if len(tokens) != 1 {
		t.Errorf("expected upsert to keep 1 token, got %d", len(tokens))
	}
	if tokens[0].AppID != "app.v2" {
		t.Errorf("expected appId to be updated to app.v2, got %s", tokens[0].AppID)
	}
}

func TestBlocksAddAndCheck(t *testing.T) {
	s := newTestStore(t)

	s.AddBlock("did:plc:alice", "did:plc:bob", "")

	tests := []struct {
		name     string
		actor    string
		target   string
		expected bool
	}{
		{"target blocked actor", "did:plc:bob", "did:plc:alice", true},
		{"actor blocked target", "did:plc:alice", "did:plc:bob", true},
		{"no block relationship", "did:plc:alice", "did:plc:carol", false},
		{"reversed no block", "did:plc:carol", "did:plc:alice", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.IsBlocked(tt.actor, tt.target); got != tt.expected {
				t.Errorf("IsBlocked(%s, %s) = %v, want %v", tt.actor, tt.target, got, tt.expected)
			}
		})
	}
}

func TestBlockRemove(t *testing.T) {
	s := newTestStore(t)

	s.AddBlock("did:plc:alice", "did:plc:bob", "")
	s.RemoveBlock("did:plc:alice", "did:plc:bob")

	if s.IsBlocked("did:plc:alice", "did:plc:bob") {
		t.Error("expected block to be removed")
	}
}

func TestGetStats(t *testing.T) {
	s := newTestStore(t)

	s.RegisterToken("did:plc:alice", "ios", "token1", "app.test")
	s.RegisterToken("did:plc:bob", "android", "token2", "app.test")
	s.AddBlock("did:plc:alice", "did:plc:carol", "")

	tokens, blocks, dids := s.GetStats()
	if tokens != 2 {
		t.Errorf("expected 2 tokens, got %d", tokens)
	}
	if blocks != 1 {
		t.Errorf("expected 1 block, got %d", blocks)
	}
	if dids != 2 {
		t.Errorf("expected 2 DIDs, got %d", dids)
	}
}

func TestPersistenceAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// First instance: register
	s1, _ := New(dbPath)
	s1.RegisterToken("did:plc:alice", "ios", "token1", "app.test")
	s1.AddBlock("did:plc:alice", "did:plc:bob", "rkey1")
	s1.Close()

	// Second instance: should load from disk
	s2, _ := New(dbPath)
	defer s2.Close()

	if !s2.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to persist across restart")
	}
	if !s2.IsBlocked("did:plc:alice", "did:plc:bob") {
		t.Error("expected block to persist across restart")
	}
}

func TestRegisterToken_EnforcesPerDIDLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Register up to the cap — should succeed.
	for i := 0; i < 20; i++ {
		tok := fmt.Sprintf("token-%d", i)
		if err := s.RegisterToken("did:plc:alice", "ios", tok, "app"); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	// 21st should fail.
	if err := s.RegisterToken("did:plc:alice", "ios", "token-21", "app"); err == nil {
		t.Error("expected error on 21st token, got nil")
	}
}
