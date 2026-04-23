package xrpc

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/did"
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
	h := NewHandlerWithoutStats(s, true, "did:web:push.example.org") // dev mode
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
	h := NewHandlerWithoutStats(s, false, "did:web:push.example.org") // production mode

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

// mockResolver is a DIDResolver that returns a fixed DID document.
type mockResolver struct {
	docs map[string]*did.DIDDocument
	err  error
}

func (m *mockResolver) ResolveDID(d string) (*did.DIDDocument, error) {
	if m.err != nil {
		return nil, m.err
	}
	doc, ok := m.docs[d]
	if !ok {
		return nil, fmt.Errorf("unknown DID: %s", d)
	}
	return doc, nil
}

// mintTestJWT signs a JWT (ES256) with the given key, returning the compact form.
// Fields left zero-valued are omitted from the payload.
func mintTestJWT(t *testing.T, key *ecdsa.PrivateKey, iss, aud, lxm string, exp int64, alg string) string {
	t.Helper()
	if alg == "" {
		alg = "ES256"
	}
	header := map[string]string{"alg": alg, "typ": "JWT"}
	headerJSON, _ := json.Marshal(header)
	claims := map[string]interface{}{}
	if iss != "" {
		claims["iss"] = iss
	}
	if aud != "" {
		claims["aud"] = aud
	}
	if lxm != "" {
		claims["lxm"] = lxm
	}
	if exp != 0 {
		claims["exp"] = exp
	}
	claimsJSON, _ := json.Marshal(claims)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerB64 + "." + claimsB64

	var sigB64 string
	switch alg {
	case "none":
		sigB64 = ""
	default:
		hash := sha256.Sum256([]byte(signingInput))
		r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		keySize := (key.Curve.Params().BitSize + 7) / 8
		sig := make([]byte, 2*keySize)
		r.FillBytes(sig[:keySize])
		s.FillBytes(sig[keySize:])
		sigB64 = base64.RawURLEncoding.EncodeToString(sig)
	}
	return signingInput + "." + sigB64
}

// makeTestKeyAndDoc generates a P-256 key and a DID document that advertises it.
func makeTestKeyAndDoc(t *testing.T, didStr string) (*ecdsa.PrivateKey, *did.DIDDocument) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	xB64 := base64.RawURLEncoding.EncodeToString(key.PublicKey.X.FillBytes(make([]byte, 32)))
	yB64 := base64.RawURLEncoding.EncodeToString(key.PublicKey.Y.FillBytes(make([]byte, 32)))
	doc := &did.DIDDocument{
		ID: didStr,
		VerificationMethod: []did.VerificationMethod{
			{
				ID:         didStr + "#atproto",
				Type:       "JsonWebKey2020",
				Controller: didStr,
				PublicKeyJwk: &did.JWK{
					Kty: "EC",
					Crv: "P-256",
					X:   xB64,
					Y:   yB64,
				},
			},
		},
	}
	return key, doc
}

// newProdHandler creates a non-dev-mode handler with a mock DID resolver.
func newProdHandler(t *testing.T, serviceDID string, resolver *mockResolver) (*Handler, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandlerWithoutStats(s, false, serviceDID) // production mode
	h.didResolver = resolver
	return h, s
}

func TestRegisterPush_RejectsWrongAud(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	_ = key
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:different.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]",
		Platform:   "ios",
		AppID:      "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for wrong aud, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_AcceptsCorrectAud(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, s := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]",
		Platform:   "ios",
		AppID:      "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200 with correct aud, got %d: %s", w.Code, w.Body.String())
	}
	if !s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be registered")
	}
}

func TestRegisterPush_RejectsMissingLXM(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"", time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]",
		Platform:   "ios",
		AppID:      "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/"+lexiconRegisterPush, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for missing lxm, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnregisterPush_RejectsWrongLXM(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, s := newProdHandler(t, "did:web:push.example.org", r)

	if err := s.RegisterToken("did:plc:alice", "ios", "token1", "app.test"); err != nil {
		t.Fatalf("register seed token: %v", err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		lexiconRegisterPush, time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "token1",
		Platform:   "ios",
		AppID:      "app.test",
	})
	req := httptest.NewRequest("POST", "/xrpc/"+lexiconUnregisterPush, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for wrong lxm, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnregisterPush_AcceptsCorrectLXM(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, s := newProdHandler(t, "did:web:push.example.org", r)

	if err := s.RegisterToken("did:plc:alice", "ios", "token1", "app.test"); err != nil {
		t.Fatalf("register seed token: %v", err)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		lexiconUnregisterPush, time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "token1",
		Platform:   "ios",
		AppID:      "app.test",
	})
	req := httptest.NewRequest("POST", "/xrpc/"+lexiconUnregisterPush, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("expected 200 with correct lxm, got %d: %s", w.Code, w.Body.String())
	}
	if s.IsRegistered("did:plc:alice") {
		t.Error("expected did:plc:alice to be unregistered")
	}
}

func TestRegisterPush_RejectsNoneAlg(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	_ = key
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	// alg="none" with no signature
	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "none")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for alg=none, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsHS256Alg(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	_ = key
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "HS256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for alg=HS256, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsResolutionFailure(t *testing.T) {
	key, _ := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{err: fmt.Errorf("plc.directory down")}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 when DID resolution fails, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsSignatureMismatch(t *testing.T) {
	// Sign with key A, advertise key B in DID doc → verification must fail.
	_, docA := makeTestKeyAndDoc(t, "did:plc:alice")
	keyB, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": docA}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	jwt := mintTestJWT(t, keyB, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(60*time.Second).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for bad signature, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsExpTooFarInFuture(t *testing.T) {
	key, doc := makeTestKeyAndDoc(t, "did:plc:alice")
	r := &mockResolver{docs: map[string]*did.DIDDocument{"did:plc:alice": doc}}
	h, _ := newProdHandler(t, "did:web:push.example.org", r)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	// exp is 10 minutes in the future — exceeds 5-minute cap
	jwt := mintTestJWT(t, key, "did:plc:alice", "did:web:push.example.org",
		"app.bsky.notification.registerPush", time.Now().Add(10*time.Minute).Unix(), "ES256")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("expected 401 for exp too far in future, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsServiceDIDMismatch(t *testing.T) {
	h, _ := newTestHandler(t) // dev mode, serviceDID = did:web:push.example.org

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:wrong.example.org", // mismatch
		Token:      "ExponentPushToken[x]", Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for serviceDid mismatch, got %d", w.Code)
	}
}

func TestRegisterPush_RejectsOversizedBody(t *testing.T) {
	h, _ := newTestHandler(t)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	// 256 KiB of junk — well over the 64 KiB cap.
	huge := make([]byte, 256*1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body := []byte(`{"serviceDid":"did:web:push.example.org","token":"`)
	body = append(body, huge...)
	body = append(body, []byte(`","platform":"ios","appId":"app"}`)...)

	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 && w.Code != 413 {
		t.Errorf("expected 400 or 413 for oversized body, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterPush_RejectsOversizedToken(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	// 3 KiB token — over 2 KiB cap
	token := strings.Repeat("a", 3*1024)
	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      token, Platform: "ios", AppID: "app",
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for oversized token, got %d", w.Code)
	}
}

func TestRegisterPush_RejectsOversizedAppID(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, "did:web:push.example.org")

	appID := strings.Repeat("a", 512)
	body, _ := json.Marshal(RegisterPushRequest{
		ServiceDID: "did:web:push.example.org",
		Token:      "t", Platform: "ios", AppID: appID,
	})
	req := httptest.NewRequest("POST", "/xrpc/app.bsky.notification.registerPush", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-DID", "did:plc:alice")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("expected 400 for oversized appId, got %d", w.Code)
	}
}

func TestMaybeStartBlocksBackfill_OnlyRunsOncePerDID(t *testing.T) {
	h, _ := newTestHandler(t)

	// First call — claims and runs (may hit the real AppView, which is OK — we ignore).
	h.maybeStartBlocksBackfill("did:plc:alice")
	// Second call — should no-op because already marked.
	h.maybeStartBlocksBackfill("did:plc:alice")

	// Assert the claim semantics: a fresh MarkBlocksBackfilled call returns false.
	claimed, err := h.store.MarkBlocksBackfilled("did:plc:alice")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("expected did:plc:alice to already be marked as backfilled")
	}
}
