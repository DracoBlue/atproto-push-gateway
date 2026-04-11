package xrpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

func newTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandlerWithoutStats(s, true) // dev mode
	return h, s
}

func TestRegisterPush(t *testing.T) {
	h, s := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[test123]",
		Platform:   "ios",
		AppID:      "org.example.app",
	})

	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice") // dev mode auth
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be registered after registerPush")
	}
}

func TestRegisterPushMissingFields(t *testing.T) {
	h, _ := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	tests := []struct {
		name string
		body RegisterPushRequest
	}{
		{"missing serviceDid", RegisterPushRequest{Token: "t", Platform: "ios", AppID: "a"}},
		{"missing token", RegisterPushRequest{ServiceDID: "d", Platform: "ios", AppID: "a"}},
		{"missing platform", RegisterPushRequest{ServiceDID: "d", Token: "t", AppID: "a"}},
		{"missing appId", RegisterPushRequest{ServiceDID: "d", Token: "t", Platform: "ios"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Actor-DID", "did:plc:alice")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != 400 {
				t.Errorf("expected 400 for %s, got %d", tt.name, w.Code)
			}
		})
	}
}

func TestRegisterPushInvalidPlatform(t *testing.T) {
	h, _ := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "token",
		Platform:   "windows",
		AppID:      "app",
	})

	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for invalid platform, got %d", w.Code)
	}
}

func TestUnregisterPush(t *testing.T) {
	h, s := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	// First register
	s.RegisterToken("did:plc:alice", "ios", "token1", "app.test")

	// Then unregister
	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "token1",
		Platform:   "ios",
		AppID:      "app.test",
	})

	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.unregisterPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200, got %d", w.Code)
	}

	if s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be unregistered")
	}
}

func TestRegisterPushNoAuth(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, _ := store.New(dbPath)
	defer s.Close()
	h := NewHandlerWithoutStats(s, false) // production mode, no dev mode

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "token",
		Platform:   "ios",
		AppID:      "app",
	})

	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No auth header
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

func TestDIDDocument(t *testing.T) {
	h, _ := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	req := httptest.NewRequest("GET", "/.well-known/did.json", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var doc map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if doc["id"] != "did:web:push.example.org" {
		t.Errorf("expected id did:web:push.example.org, got %v", doc["id"])
	}

	services, ok := doc["service"].([]interface{})
	if !ok || len(services) != 1 {
		t.Fatal("expected 1 service entry")
	}

	svc := services[0].(map[string]interface{})
	if svc["id"] != "#bsky_notif" {
		t.Errorf("expected service id #bsky_notif, got %v", svc["id"])
	}
	if svc["type"] != "BskyNotificationService" {
		t.Errorf("expected type BskyNotificationService, got %v", svc["type"])
	}
	if svc["serviceEndpoint"] != "https://push.example.org" {
		t.Errorf("expected endpoint https://push.example.org, got %v", svc["serviceEndpoint"])
	}
}

func TestHealthEndpoint(t *testing.T) {
	h, s := newTestHandler(t)

	s.RegisterToken("did:plc:alice", "ios", "token1", "app.test")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var health map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &health)

	if health["status"] != "ok" {
		t.Errorf("expected status ok, got %v", health["status"])
	}
	if health["registeredDIDs"].(float64) != 1 {
		t.Errorf("expected 1 registered DID, got %v", health["registeredDIDs"])
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h, _ := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	req := httptest.NewRequest("GET", "/xrpc/app.bsky.notification.registerPush", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 405 {
		t.Errorf("expected 405, got %d", w.Code)
	}
}
