# Security Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix 15 findings (C1–C6, I1–I5, I9–I11, I14) from the code review so this gateway is safe to expose to the public internet and comfortable for a few dozen users.

**Architecture:** Each finding lands as its own commit on the `security-hardening` branch. TDD where practical; for pure configuration changes (timeouts, Dockerfile), add a smoke test that asserts the config is set rather than exercising the behavior.

**Tech Stack:** Go 1.25, standard library `net/http`, `database/sql`, `gorilla/websocket`, `golang-jwt/jwt/v5`, `mattn/go-sqlite3`. Tests use standard `testing` plus `net/http/httptest`.

---

## Project Context (read before every task)

`atproto-push-gateway` is a self-hosted push-notification gateway for ATProto. Directory layout:

```
cmd/server/main.go                     wiring, config, HTTP server
internal/xrpc/handler.go               XRPC handlers + JWT verification (attack surface)
internal/xrpc/handler_test.go          existing tests (use dev-mode X-Actor-DID bypass)
internal/did/resolver.go               DID resolution (plc.directory + did:web)
internal/did/resolver_test.go
internal/jetstream/consumer.go         WebSocket consumer, event matching, push dispatch
internal/jetstream/consumer_test.go
internal/push/push.go                  MultiSender + Expo Push
internal/push/apns.go                  APNs HTTP/2 sender
internal/push/fcm.go                   FCM v1 sender
internal/store/store.go                SQLite layer + in-memory indexes
internal/store/store_test.go
internal/profile/resolver.go           profile cache (AppView)
```

**Request flow:** User's PDS forwards `app.bsky.notification.registerPush` to this gateway with an inter-service JWT. The gateway verifies the JWT (iss DID → plc.directory or did:web → signing key → ES256/ES256K signature), persists the token in SQLite, and kicks off Jetstream consumption. Jetstream events are matched against registered DIDs, filtered through a block graph, then pushed via APNs/FCM/Expo.

**Running the tests:**
```bash
cd /home/yolo/Projects/atproto-push-gateway
go test ./...
```

**Running a specific test:**
```bash
go test ./internal/xrpc -run TestName -v
```

**Building:**
```bash
go build ./...
```

## Shared Helpers (added in Task 1, used by several later tasks)

Task 1 introduces a test helper that mints a valid ES256 JWT with a generated key pair, plus a mockable `DIDResolver` interface. Tasks 2 and 3 depend on these. Each task that uses them includes a pointer back to Task 1.

---

## Task 1: C1 — Validate JWT `aud` claim

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/xrpc/handler_test.go`

**Context:** `verifyAuth` parses the `aud` field into `jwtClaims.Aud` but never checks it. An attacker who captures a legitimate inter-service JWT minted for *any other service* (e.g. the PDS→AppView JWT) can replay it here and register a push token for the user. We must reject JWTs whose `aud` doesn't equal the configured `serviceDID`.

This task also adds two shared helpers that later tasks will reuse:
1. A `DIDResolver` interface on the `Handler` so tests can inject a mock resolver.
2. A test helper `mintTestJWT` that signs a valid ES256 JWT with a generated key pair.

### Steps

- [ ] **Step 1: Write failing tests**

Add to `internal/xrpc/handler_test.go` (append at the bottom):

```go
import (
	// existing imports
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/did"
)

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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/xrpc -run TestRegisterPush_RejectsWrongAud -v
go test ./internal/xrpc -run TestRegisterPush_AcceptsCorrectAud -v
```

Expected: both FAIL (compile error — `NewHandlerWithoutStats` signature, `didResolver` field unexported).

- [ ] **Step 3: Update handler to accept serviceDID and make resolver mockable**

Edit `internal/xrpc/handler.go`:

Add a `DIDResolver` interface (place near the top, after imports):

```go
// DIDResolver resolves a DID to a DID document. Abstracted as an interface
// so tests can inject a fake resolver without hitting the network.
type DIDResolver interface {
	ResolveDID(did string) (*did.DIDDocument, error)
}
```

Change the `Handler` struct to add `serviceDID` and change resolver to the interface:

```go
type Handler struct {
	store              *store.Store
	devMode            bool
	serviceDID         string
	statsProvider      StatsProvider
	didResolver        DIDResolver
	onTokenRegistered  func()
}
```

Update constructors:

```go
func NewHandler(s *store.Store, devMode bool, serviceDID string, sp StatsProvider, onTokenRegistered func()) *Handler {
	return &Handler{store: s, devMode: devMode, serviceDID: serviceDID, statsProvider: sp, didResolver: did.NewResolver(), onTokenRegistered: onTokenRegistered}
}

func NewHandlerWithoutStats(s *store.Store, devMode bool, serviceDID string) *Handler {
	return &Handler{store: s, devMode: devMode, serviceDID: serviceDID, didResolver: did.NewResolver(), onTokenRegistered: nil}
}
```

In `verifyAuth`, after the `iss` validation block (around line 217), add the `aud` check:

```go
	if claims.Aud != h.serviceDID {
		return "", fmt.Errorf("JWT aud mismatch: got %q, want %q", claims.Aud, h.serviceDID)
	}
```

- [ ] **Step 4: Update main.go to pass serviceDID**

In `cmd/server/main.go` replace:

```go
handler := xrpc.NewHandler(s, devMode, func() interface{} { return consumer.GetStats() }, consumer.NotifyTokenRegistered)
```

with:

```go
handler := xrpc.NewHandler(s, devMode, serviceDID, func() interface{} { return consumer.GetStats() }, consumer.NotifyTokenRegistered)
```

- [ ] **Step 5: Update existing tests to pass serviceDID**

In `internal/xrpc/handler_test.go`, update `newTestHandler`:

```go
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
```

And fix `TestRegisterPushNoAuth`:

```go
	h := NewHandlerWithoutStats(s, false, "did:web:push.example.org") // production mode
```

- [ ] **Step 6: Run all xrpc tests**

```bash
go test ./internal/xrpc -v
```

Expected: all pass (including the two new ones).

- [ ] **Step 7: Run full build and test**

```bash
go build ./... && go test ./...
```

Expected: no failures.

- [ ] **Step 8: Commit**

```bash
git add internal/xrpc/handler.go internal/xrpc/handler_test.go cmd/server/main.go
git commit -m "fix(xrpc): validate JWT aud claim against configured service DID

Reject inter-service JWTs whose aud doesn't match PUSH_GATEWAY_DID.
Previously the aud claim was parsed but never checked, allowing replay
of legitimate JWTs minted for other services.

Also introduces a DIDResolver interface so tests can inject a fake
resolver, plus a test helper that mints ES256 JWTs with a generated
key pair. Both are reused by later hardening commits."
```

---

## Task 2: C2 — Reject JWTs on any verification failure (no silent downgrade)

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `internal/xrpc/handler_test.go`

**Context:** Four branches in `verifyAuth` log a warning and `return claims.Iss, nil` without verifying the signature: DID resolution fails, key extraction fails, curve mismatch, unknown algorithm. All four must become hard errors. Also add an explicit algorithm allow-list (`ES256`, `ES256K`) that fires *before* DID resolution, so an attacker can't drive the resolver with an unsigned/HS256 JWT.

### Steps

- [ ] **Step 1: Write failing tests**

Append to `internal/xrpc/handler_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/xrpc -run "TestRegisterPush_Rejects" -v
```

Expected: all four NEW tests FAIL (the current code accepts all these cases).

- [ ] **Step 3: Fix verifyAuth**

Edit `internal/xrpc/handler.go`. After the header is parsed (at the point where `var header jwtHeader` is filled in), add the algorithm allow-list check *before* any DID resolution:

```go
	// Enforce explicit algorithm allow-list. ATProto uses ES256/ES256K only.
	// Rejects "none", "HS256", "RS256", etc. before any key material is loaded.
	if header.Alg != "ES256" && header.Alg != "ES256K" {
		return "", fmt.Errorf("unsupported JWT algorithm: %q", header.Alg)
	}
```

Then replace every `return claims.Iss, nil` inside the resolver block with `return "", fmt.Errorf(...)`. The complete replaced block should read:

```go
	// 2. Resolve the issuer DID to get the public key
	if h.didResolver == nil {
		return "", fmt.Errorf("no DID resolver configured")
	}
	doc, err := h.didResolver.ResolveDID(claims.Iss)
	if err != nil {
		return "", fmt.Errorf("could not resolve DID %s: %w", claims.Iss, err)
	}

	pubKey, err := did.GetSigningKey(doc)
	if err != nil {
		return "", fmt.Errorf("could not extract signing key for %s: %w", claims.Iss, err)
	}

	// 3. Verify the signature
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT signature: %w", err)
	}

	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))

	verified := false
	switch header.Alg {
	case "ES256K":
		if pubKey.Curve == elliptic.P256() {
			return "", fmt.Errorf("ES256K JWT but got P-256 key for %s", claims.Iss)
		}
		verified = verifyECDSASignature(pubKey, hash[:], sigBytes)
	case "ES256":
		if pubKey.Curve != elliptic.P256() {
			return "", fmt.Errorf("ES256 JWT but key curve mismatch for %s", claims.Iss)
		}
		verified = verifyECDSASignature(pubKey, hash[:], sigBytes)
	}

	if !verified {
		return "", fmt.Errorf("JWT signature verification failed for %s", claims.Iss)
	}

	log.Printf("[xrpc] JWT signature verified for %s (alg=%s)", claims.Iss, header.Alg)
	return claims.Iss, nil
```

Note: the outer `if h.didResolver != nil { ... return claims.Iss, nil }` wrapper is gone — we no longer support a nil-resolver fallthrough.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/xrpc -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/xrpc/handler.go internal/xrpc/handler_test.go
git commit -m "fix(xrpc): reject JWTs on any verification failure

Removes four silent-downgrade branches in verifyAuth that previously
accepted the JWT without signature verification when DID resolution
failed, key extraction failed, the curve didn't match the alg, or the
alg was unknown. All become hard errors.

Adds an explicit ES256/ES256K algorithm allow-list that fires before
DID resolution, so attacker-controlled alg values (none, HS256, RS256)
can't drive the resolver."
```

---

## Task 3: C3 — Cap JWT `exp` at 5 minutes in the future

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `internal/xrpc/handler_test.go`

**Context:** The current code accepts any `exp` in the future. Bluesky inter-service JWTs are typically ~60 s lifetime; a 5-minute cap leaves generous headroom for clock skew and odd PDS implementations while preventing long-lived replay. Uses helpers from Task 1.

### Steps

- [ ] **Step 1: Write failing test**

Append to `internal/xrpc/handler_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/xrpc -run TestRegisterPush_RejectsExpTooFarInFuture -v
```

Expected: FAIL.

- [ ] **Step 3: Add exp cap check**

In `internal/xrpc/handler.go`, in `verifyAuth`, replace:

```go
	if time.Now().Unix() > claims.Exp {
		return "", fmt.Errorf("JWT expired")
	}
```

with:

```go
	now := time.Now().Unix()
	if now > claims.Exp {
		return "", fmt.Errorf("JWT expired")
	}
	const maxLifetimeSeconds = 300 // 5 minutes
	if claims.Exp-now > maxLifetimeSeconds {
		return "", fmt.Errorf("JWT exp too far in future (%ds > %ds)", claims.Exp-now, maxLifetimeSeconds)
	}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/xrpc -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/xrpc/handler.go internal/xrpc/handler_test.go
git commit -m "fix(xrpc): cap JWT exp at 5 minutes in the future

Inter-service JWTs from PDSes typically have ~60s lifetime. Reject any
token whose exp is more than 5 minutes ahead of now, to bound the
replay window for stolen tokens."
```

---

## Task 4: C6 — Validate `serviceDid` in request body matches configured DID

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `internal/xrpc/handler_test.go`

**Context:** The request body's `serviceDid` field is required to be non-empty but never compared against the gateway's own configured DID. Misconfigured clients (or attackers trying to fingerprint the gateway) can register against any `serviceDid`. Reject mismatches with 400.

### Steps

- [ ] **Step 1: Write failing test**

Append to `internal/xrpc/handler_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/xrpc -run TestRegisterPush_RejectsServiceDIDMismatch -v
```

Expected: FAIL (currently returns 200).

- [ ] **Step 3: Add the check in both handlers**

In `internal/xrpc/handler.go`, in `handleRegisterPush`, after the missing-fields check, add:

```go
	if req.ServiceDID != h.serviceDID {
		http.Error(w, `{"error":"invalid_request","message":"serviceDid does not match this gateway"}`, http.StatusBadRequest)
		return
	}
```

Add the same check in `handleUnregisterPush` after its missing-fields check.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/xrpc -v
```

Expected: all pass. Existing tests already use `did:web:push.example.org` so they continue to work.

- [ ] **Step 5: Commit**

```bash
git add internal/xrpc/handler.go internal/xrpc/handler_test.go
git commit -m "fix(xrpc): require request body serviceDid to match configured DID

Reject registerPush/unregisterPush calls whose body.serviceDid doesn't
equal PUSH_GATEWAY_DID. Catches misrouted forwards and forces clients
to be explicit about which gateway they intend to register with."
```

---

## Task 5: C5 — Strict multibase key length

**Files:**
- Modify: `internal/did/resolver.go`
- Modify: `internal/did/resolver_test.go`

**Context:** `parseMultibaseKey` checks `len(decoded) >= 35` (2-byte prefix + 33-byte compressed key). Trailing garbage is silently ignored. Require exact equality so malformed keys fail fast.

### Steps

- [ ] **Step 1: Write failing test**

Append to `internal/did/resolver_test.go`:

```go
func TestParseMultibaseKey_RequiresExactLength(t *testing.T) {
	// 36 bytes: 2-byte p256-pub prefix + 34-byte "compressed" key. Must be
	// rejected because total length isn't 35 (the exact size of a valid key).
	keyBytes := make([]byte, 36)
	keyBytes[0] = 0x80
	keyBytes[1] = 0x24
	keyBytes[2] = 0x02
	for i := 3; i < 36; i++ {
		keyBytes[i] = 0xbb
	}

	encoded := "z" + base58Encode(keyBytes)

	_, err := parseMultibaseKey("Multikey", encoded)
	if err == nil {
		t.Error("expected error for multibase key of wrong total length, got nil")
	}
}

// base58Encode is a minimal test helper (encodes using the Bitcoin alphabet).
func base58Encode(b []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	x := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	mod := new(big.Int)
	var result []byte
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		result = append([]byte{alphabet[mod.Int64()]}, result...)
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		result = append([]byte{'1'}, result...)
	}
	return string(result)
}
```

Add `"math/big"` to the test file's imports if not already there.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/did -run TestParseMultibaseKey_RequiresExactLength -v
```

Expected: FAIL. The current code accepts any `len(decoded) >= 35`, then slices `decoded[2:]` as the compressed key (34 bytes), and relies on the inner `!= 33` check to fail — but the error path is non-uniform. We want a clean up-front length check.

- [ ] **Step 3: Tighten parseMultibaseKey**

In `internal/did/resolver.go`, change both `len(decoded) >= 35` checks to `len(decoded) == 35`:

```go
	if len(decoded) == 35 && decoded[0] == 0xe7 && decoded[1] == 0x01 {
```

and

```go
	if len(decoded) == 35 && decoded[0] == 0x80 && decoded[1] == 0x24 {
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/did -v
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/did/resolver.go internal/did/resolver_test.go
git commit -m "fix(did): require exact length for multibase-encoded keys

parseMultibaseKey now requires len(decoded) == 35 (2-byte multicodec
prefix + 33-byte compressed public key) instead of >= 35. Defensive
hygiene — once a key parser falls through to the error path, callers
must treat that as authentication failure (not a soft accept)."
```

---

## Task 6: C4 — Reject did:web resolution to private/loopback/link-local addresses

**Files:**
- Modify: `internal/did/resolver.go`
- Modify: `internal/did/resolver_test.go`

**Context:** `fetchDIDDocument` blindly builds `https://<did:web-suffix>/.well-known/did.json` and GETs it with the default `http.Client`. An attacker-crafted `did:web:127.0.0.1:8080` or `did:web:169.254.169.254` (cloud IMDS) turns the gateway into an SSRF proxy. Add a custom `DialContext` that rejects RFC1918 / loopback / link-local addresses, and cap redirects.

### Steps

- [ ] **Step 1: Write failing test**

Append to `internal/did/resolver_test.go`:

```go
func TestResolveDIDWeb_RejectsLoopback(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:127.0.0.1")
	if err == nil {
		t.Error("expected error resolving did:web:127.0.0.1, got nil")
	}
}

func TestResolveDIDWeb_RejectsLocalhost(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:localhost")
	if err == nil {
		t.Error("expected error resolving did:web:localhost, got nil")
	}
}

func TestResolveDIDWeb_RejectsIMDS(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:169.254.169.254")
	if err == nil {
		t.Error("expected error resolving did:web:169.254.169.254 (IMDS), got nil")
	}
}

func TestResolveDIDWeb_RejectsRFC1918(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:10.0.0.1")
	if err == nil {
		t.Error("expected error resolving did:web:10.0.0.1, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/did -run "TestResolveDIDWeb_Rejects" -v
```

Expected: all four FAIL — current code attempts the HTTP request and typically returns a different error (connection refused, etc.) rather than rejecting the hostname up front. Even if they happen to produce *some* error, the error won't identify the SSRF rejection. We want a deterministic rejection.

Tighten the assertions to check the error message includes "blocked" or similar:

```go
func TestResolveDIDWeb_RejectsLoopback(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error, got: %v", err)
	}
}
```

Apply the same `|| !strings.Contains(err.Error(), "blocked")` to the other three. Ensure `strings` is in the import list.

- [ ] **Step 3: Implement SSRF protection**

Edit `internal/did/resolver.go`.

Add imports: `"context"`, `"net"`.

Replace the `NewResolver` implementation with:

```go
func NewResolver() *Resolver {
	// Custom transport that blocks resolution to private / loopback /
	// link-local IP addresses for SSRF protection. Applied on every dial,
	// including redirects.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isBlockedIP(ip.IP) {
					return nil, fmt.Errorf("blocked IP %s for host %s (SSRF protection)", ip.IP, host)
				}
			}
			// Dial the first non-blocked IP explicitly to avoid DNS-rebinding.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	return &Resolver{
		cache:  make(map[string]cacheEntry),
		client: client,
	}
}

// isBlockedIP returns true for addresses the resolver must refuse to connect
// to: loopback, link-local (incl. AWS/GCP/Azure IMDS at 169.254.169.254),
// multicast, and RFC1918 private IPv4 plus IPv6 ULA/site-local.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	return false
}
```

Also block the obvious hostname case explicitly. In `fetchDIDDocument`, after computing `domain`, add:

```go
		if domain == "localhost" || strings.HasPrefix(domain, "localhost:") || strings.HasPrefix(domain, "localhost/") {
			return nil, fmt.Errorf("blocked hostname %q (SSRF protection)", domain)
		}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/did -v
```

Expected: all pass (new SSRF tests + existing resolver tests).

Note: `net.IP.IsPrivate()` was added in Go 1.17 and covers RFC1918 IPv4 and IPv6 ULA (fc00::/7). `net.IP.IsLinkLocalUnicast()` covers 169.254.0.0/16 and fe80::/10.

- [ ] **Step 5: Commit**

```bash
git add internal/did/resolver.go internal/did/resolver_test.go
git commit -m "fix(did): block SSRF via did:web to private/loopback/IMDS addresses

The did:web suffix is interpolated directly into an HTTPS URL.
Attacker-controlled JWTs with iss=did:web:127.0.0.1 or
did:web:169.254.169.254 (cloud IMDS) previously caused the gateway to
issue outbound GETs to internal addresses.

Adds a custom Transport with a DialContext that refuses to connect to
loopback, link-local (including IMDS), multicast, unspecified, or
RFC1918/ULA addresses, applied on every dial including redirects.
Caps redirects at 3. Also rejects 'localhost' as a hostname before
any DNS lookup."
```

---

## Task 7: I2 — Cap JSON body sizes on XRPC endpoints and DID document fetch

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `internal/did/resolver.go`
- Modify: `internal/xrpc/handler_test.go`

**Context:** `json.NewDecoder(r.Body).Decode` streams the whole body; an attacker can POST gigabytes. The DID doc fetch is similar via `json.NewDecoder(resp.Body).Decode`. Wrap with `http.MaxBytesReader` (handlers) and `io.LimitReader` (DID fetch).

### Steps

- [ ] **Step 1: Write failing test**

Append to `internal/xrpc/handler_test.go`:

```go
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
```

- [ ] **Step 2: Run to verify it fails**

```bash
go test ./internal/xrpc -run TestRegisterPush_RejectsOversizedBody -v
```

Expected: FAIL (currently returns 200 because the body is valid JSON, just huge).

- [ ] **Step 3: Cap body sizes in handlers**

In `internal/xrpc/handler.go`, at the top of `handleRegisterPush` (before `json.NewDecoder`):

```go
	const maxBodyBytes = 64 * 1024 // 64 KiB
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
```

Same at the top of `handleUnregisterPush`.

- [ ] **Step 4: Cap body size in DID doc fetch**

In `internal/did/resolver.go`, change the decoding block in `fetchDIDDocument`:

```go
	const maxDocBytes = 256 * 1024 // 256 KiB
	limited := io.LimitReader(resp.Body, maxDocBytes)
	var doc DIDDocument
	if err := json.NewDecoder(limited).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode DID document for %s: %w", did, err)
	}
```

Add `"io"` to the imports if missing.

- [ ] **Step 5: Run tests**

```bash
go test ./... -v
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/xrpc/handler.go internal/did/resolver.go internal/xrpc/handler_test.go
git commit -m "fix: cap XRPC body size and DID document size

Previously json.NewDecoder streamed request bodies of any size,
letting a single POST consume unbounded memory/CPU. Wrap XRPC handler
bodies with http.MaxBytesReader (64 KiB) and DID document responses
with io.LimitReader (256 KiB)."
```

---

## Task 8: I1 — HTTP server timeouts

**Files:**
- Modify: `cmd/server/main.go`

**Context:** The current `http.Server` has no timeouts — a slowloris peer can hold a goroutine forever. Add `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, `MaxHeaderBytes`.

### Steps

- [ ] **Step 1: Modify main.go**

In `cmd/server/main.go`, replace:

```go
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
```

with:

```go
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16, // 64 KiB
	}
```

- [ ] **Step 2: Verify the build**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "fix(server): add HTTP timeouts and header size cap

Previously the http.Server had no timeouts, so slowloris clients could
hold connection goroutines indefinitely. Adds 10s read-header, 30s
read/write, 120s idle, and 64 KiB max-header-bytes."
```

---

## Task 9: I9 — DEV_MODE guardrails

**Files:**
- Modify: `cmd/server/main.go`

**Context:** `DEV_MODE=true` enables `/test/register` (no auth) and an `X-Actor-DID` header bypass in the JWT verifier. If it's accidentally set in production, the gateway becomes a free notification spoofer. Add loud startup warnings and refuse to bind a non-loopback address unless a second explicit opt-in env var is set.

### Steps

- [ ] **Step 1: Modify main.go**

In `cmd/server/main.go`, right after `devMode := getEnv("DEV_MODE", "") == "true"`, add:

```go
	devModeAllowPublic := getEnv("DEV_MODE_ALLOW_PUBLIC", "") == "true"
```

After the `log.Printf("  Dev mode: %v", devMode)` line, add:

```go
	if devMode {
		log.Println("")
		log.Println("!!! DEV_MODE ENABLED — do NOT run on a public network !!!")
		log.Println("!!! /test/register accepts unauthenticated requests !!!")
		log.Println("!!! X-Actor-DID header bypasses JWT verification    !!!")
		log.Println("")
	}
```

Change the bind logic. Replace:

```go
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		...
	}
```

(the one you just added in Task 8) with:

```go
	bindAddr := ":" + port
	if devMode && !devModeAllowPublic {
		bindAddr = "127.0.0.1:" + port
		log.Printf("  DEV_MODE: binding to 127.0.0.1 only (set DEV_MODE_ALLOW_PUBLIC=true to override)")
	}

	srv := &http.Server{
		Addr:              bindAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
```

Also change the `log.Printf("  Listening on :%s", port)` to print `bindAddr` instead:

```go
		log.Printf("  Listening on %s", bindAddr)
```

- [ ] **Step 2: Build and test**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 3: Commit**

```bash
git add cmd/server/main.go
git commit -m "fix(server): guardrail DEV_MODE against accidental production use

When DEV_MODE=true:
- Print loud startup warnings noting that auth is effectively disabled
- Bind to 127.0.0.1 only by default, requiring DEV_MODE_ALLOW_PUBLIC=true
  to bind to all interfaces

Prevents a stray .env file from turning a production deployment into a
free notification spoofer."
```

---

## Task 10: I3 — Token/AppID length caps and per-DID token count cap

**Files:**
- Modify: `internal/xrpc/handler.go`
- Modify: `internal/store/store.go`
- Modify: `internal/xrpc/handler_test.go`
- Modify: `internal/store/store_test.go`

**Context:** No validation on token or appId length, and no limit on how many tokens a single DID can register. An attacker with valid JWTs can fill SQLite and slow every notification dispatch for that DID (sequential push calls). Cap token length at 2 KiB, appId at 256 bytes, and limit each DID to 20 tokens.

### Steps

- [ ] **Step 1: Write failing tests**

In `internal/xrpc/handler_test.go`, append:

```go
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
```

Ensure `"strings"` is in the import list.

In `internal/store/store_test.go`, append:

```go
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
```

Add imports if missing: `"fmt"`, `"path/filepath"`.

- [ ] **Step 2: Run to verify failures**

```bash
go test ./internal/xrpc -run "TestRegisterPush_RejectsOversized" -v
go test ./internal/store -run "TestRegisterToken_EnforcesPerDIDLimit" -v
```

Expected: all three FAIL.

- [ ] **Step 3: Add length caps in handler**

In `internal/xrpc/handler.go`, after the platform check in `handleRegisterPush` add:

```go
	const maxTokenLen = 2048
	const maxAppIDLen = 256
	if len(req.Token) > maxTokenLen {
		http.Error(w, `{"error":"invalid_request","message":"token too long"}`, http.StatusBadRequest)
		return
	}
	if len(req.AppID) > maxAppIDLen {
		http.Error(w, `{"error":"invalid_request","message":"appId too long"}`, http.StatusBadRequest)
		return
	}
```

Apply the same in `handleUnregisterPush`.

- [ ] **Step 4: Enforce per-DID cap in store**

In `internal/store/store.go`, change `RegisterToken`:

```go
const maxTokensPerDID = 20

func (s *Store) RegisterToken(actorDID, platform, pushToken, appID string) error {
	// Enforce per-DID cap. An upsert on the same (actor_did, push_token) does
	// not grow the count, so count before attempting insert.
	var existing int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM push_tokens WHERE actor_did = ? AND push_token != ?",
		actorDID, pushToken,
	).Scan(&existing); err != nil {
		return err
	}
	if existing >= maxTokensPerDID {
		return fmt.Errorf("DID %s already has %d tokens (cap: %d)", actorDID, existing, maxTokensPerDID)
	}

	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO push_tokens (actor_did, platform, push_token, app_id, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		actorDID, platform, pushToken, appID,
	)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.registeredDIDs[actorDID] = true
	s.mu.Unlock()

	return nil
}
```

Add `"fmt"` to the store's imports if missing.

- [ ] **Step 5: Run tests**

```bash
go test ./... -v
```

Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/xrpc/handler.go internal/store/store.go internal/xrpc/handler_test.go internal/store/store_test.go
git commit -m "fix: cap token/appId length and tokens-per-DID

Previously an attacker with a valid JWT could register unlimited
tokens per DID and tokens of unbounded length. Since push dispatch
iterates all tokens inline on the Jetstream goroutine, a flood of
junk tokens for one DID would stall notifications for every user.

Enforces token length <= 2 KiB, appId length <= 256, and max 20
tokens per DID."
```

---

## Task 11: I10 — FCM and Expo HTTP client timeouts

**Files:**
- Modify: `internal/push/push.go`
- Modify: `internal/push/fcm.go`

**Context:** `&http.Client{}` defaults to no timeout. A stuck FCM or Expo response hangs the caller forever. APNs already has a 10 s timeout; mirror that.

### Steps

- [ ] **Step 1: Set Expo client timeout**

In `internal/push/push.go`, change `NewExpoPushSender`:

```go
func NewExpoPushSender(accessToken string) *ExpoPushSender {
	return &ExpoPushSender{
		AccessToken: accessToken,
		Client:      &http.Client{Timeout: 10 * time.Second},
	}
}
```

Add `"time"` to imports if missing.

- [ ] **Step 2: Set FCM client timeout**

In `internal/push/fcm.go`, change the `FCMSender` construction:

```go
	return &FCMSender{
		projectID:   sa.ProjectID,
		tokenSource: creds.TokenSource,
		client:      &http.Client{Timeout: 10 * time.Second},
	}, nil
```

Add `"time"` to imports if missing.

- [ ] **Step 3: Build and test**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 4: Commit**

```bash
git add internal/push/push.go internal/push/fcm.go
git commit -m "fix(push): add 10s HTTP timeout to Expo and FCM senders

APNs already had a 10s timeout; Expo and FCM used default zero-timeout
clients, so a stuck upstream response would hang the caller
indefinitely. Mirror APNs."
```

---

## Task 12: I5 — Websocket read deadline, pong handler, and ping loop

**Files:**
- Modify: `internal/jetstream/consumer.go`

**Context:** `gorilla/websocket` has no keepalive by default. A silently-broken TCP connection hangs `conn.ReadMessage()` forever. Also install `SetReadLimit` to cap frame size.

### Steps

- [ ] **Step 1: Modify the connect method**

In `internal/jetstream/consumer.go`, replace the `connect` method's setup block. Currently:

```go
func (c *Consumer) connect(url string) error {
	log.Printf("[jetstream] connecting to %s", url)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("[jetstream] dial error: %v", err)
		return err
	}
	defer conn.Close()

	// Create zstd decoder with Jetstream dictionary for compressed messages
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderDicts(zstdDictionary))
	if err != nil {
		log.Printf("[jetstream] failed to create zstd decoder: %v", err)
		return err
	}
	defer decoder.Close()

	log.Println("[jetstream] connected (zstd compression enabled, dictionary loaded)")
```

Replace with:

```go
const (
	wsReadTimeout  = 60 * time.Second
	wsWriteTimeout = 10 * time.Second
	wsPingInterval = 20 * time.Second
	wsMaxMessageBytes = 1 << 20 // 1 MiB per frame
)

func (c *Consumer) connect(url string) error {
	log.Printf("[jetstream] connecting to %s", url)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("[jetstream] dial error: %v", err)
		return err
	}
	defer conn.Close()

	conn.SetReadLimit(wsMaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})

	// Ping loop in the background to keep the connection alive. Exits when
	// the connection is closed or the consumer is stopped.
	pingStop := make(chan struct{})
	defer close(pingStop)
	go func() {
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Printf("[jetstream] ping error: %v", err)
					_ = conn.Close()
					return
				}
			case <-pingStop:
				return
			case <-c.stopCh:
				return
			}
		}
	}()

	// Create zstd decoder with Jetstream dictionary for compressed messages
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderDicts(zstdDictionary))
	if err != nil {
		log.Printf("[jetstream] failed to create zstd decoder: %v", err)
		return err
	}
	defer decoder.Close()

	log.Println("[jetstream] connected (zstd compression enabled, dictionary loaded)")
```

- [ ] **Step 2: Build and test**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 3: Commit**

```bash
git add internal/jetstream/consumer.go
git commit -m "fix(jetstream): add websocket ping/pong and read timeouts

gorilla/websocket does not install keepalives by default. A silently
broken TCP connection (NAT rebind, ISP black hole) would hang
ReadMessage indefinitely.

- SetReadLimit(1 MiB) caps frame size
- SetReadDeadline + PongHandler enforces 60s liveness
- Background goroutine pings every 20s"
```

---

## Task 13: I11 — Remove stale push tokens on APNs 410 / FCM UNREGISTERED

**Files:**
- Modify: `internal/push/push.go`
- Modify: `internal/push/apns.go`
- Modify: `internal/push/fcm.go`
- Modify: `internal/jetstream/consumer.go`

**Context:** APNs returns 410 Gone and FCM returns `UNREGISTERED` for uninstalled-app tokens. Currently we log and move on; over time the database fills with dead tokens that get re-hit on every matched event. Expose a typed `ErrTokenInvalid`, have APNs/FCM return it, and in the Jetstream dispatch path call `store.UnregisterToken` on that error.

### Steps

- [ ] **Step 1: Define the typed error**

In `internal/push/push.go`, near the top (after imports, before types), add:

```go
// ErrTokenInvalid indicates the push provider reported the device token as
// permanently invalid (uninstalled app, revoked, etc.). Callers should
// remove the token from persistent storage.
var ErrTokenInvalid = errors.New("push token permanently invalid")
```

Add `"errors"` to imports.

- [ ] **Step 2: Map APNs 410 to ErrTokenInvalid**

In `internal/push/apns.go`, at the end of `Send`, replace:

```go
	if resp.StatusCode != 200 {
		var errResp struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("APNs returned %d: %s", resp.StatusCode, errResp.Reason)
	}
```

with:

```go
	if resp.StatusCode != 200 {
		var errResp struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		// 410 Gone or reason=Unregistered → device token is permanently invalid.
		if resp.StatusCode == 410 || errResp.Reason == "Unregistered" || errResp.Reason == "BadDeviceToken" {
			return fmt.Errorf("%w: APNs %d %s", ErrTokenInvalid, resp.StatusCode, errResp.Reason)
		}
		return fmt.Errorf("APNs returned %d: %s", resp.StatusCode, errResp.Reason)
	}
```

- [ ] **Step 3: Map FCM UNREGISTERED to ErrTokenInvalid**

In `internal/push/fcm.go`, at the end of `Send`, replace:

```go
	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("FCM returned %d: %s (%s)", resp.StatusCode, errResp.Error.Status, errResp.Error.Message)
	}
```

with:

```go
	if resp.StatusCode != 200 {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		// UNREGISTERED / INVALID_ARGUMENT with bad-token details → permanent.
		if errResp.Error.Status == "UNREGISTERED" || errResp.Error.Status == "NOT_FOUND" {
			return fmt.Errorf("%w: FCM %s %s", ErrTokenInvalid, errResp.Error.Status, errResp.Error.Message)
		}
		return fmt.Errorf("FCM returned %d: %s (%s)", resp.StatusCode, errResp.Error.Status, errResp.Error.Message)
	}
```

- [ ] **Step 4: Have the consumer react to ErrTokenInvalid**

In `internal/jetstream/consumer.go`, in `sendNotification`, replace:

```go
		if err := c.sender.Send(n); err != nil {
			c.pushErrors.Add(1)
			log.Printf("[jetstream] push error for %s: %v", targetDID, err)
		} else {
			c.pushesSent.Add(1)
		}
```

with:

```go
		if err := c.sender.Send(n); err != nil {
			c.pushErrors.Add(1)
			if errors.Is(err, push.ErrTokenInvalid) {
				log.Printf("[jetstream] removing invalid token for %s: %v", targetDID, err)
				if uerr := c.store.UnregisterToken(token.ActorDID, token.Platform, token.PushToken, token.AppID); uerr != nil {
					log.Printf("[jetstream] error removing invalid token: %v", uerr)
				}
			} else {
				log.Printf("[jetstream] push error for %s: %v", targetDID, err)
			}
		} else {
			c.pushesSent.Add(1)
		}
```

Add `"errors"` to the consumer's imports.

- [ ] **Step 5: Build and test**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 6: Commit**

```bash
git add internal/push/push.go internal/push/apns.go internal/push/fcm.go internal/jetstream/consumer.go
git commit -m "fix(push): remove stale tokens on APNs 410 / FCM UNREGISTERED

Introduces ErrTokenInvalid as a typed sentinel error returned by the
APNs and FCM senders when the provider reports the device token as
permanently invalid. The Jetstream dispatcher unregisters the token
from SQLite on that error, stopping the steady rot of dead tokens
accumulating after user app-uninstalls."
```

---

## Task 14: I4 — Bounded worker pool for Jetstream push dispatch

**Files:**
- Modify: `internal/jetstream/consumer.go`

**Context:** Currently `handleCommit` and `sendNotification` run inline on the websocket read loop, so one slow push provider stalls the reader. Move dispatch to a worker pool with a bounded buffered channel. Drop events if the channel is full (log them) rather than block.

### Steps

- [ ] **Step 1: Add dispatch state to Consumer**

In `internal/jetstream/consumer.go`, in the `Consumer` struct, add two fields:

```go
	commitCh       chan dispatchItem
	eventsDropped  atomic.Int64
```

and below the `Stats` struct, change `Stats` to include `EventsDropped`:

```go
type Stats struct {
	EventsReceived int64 `json:"eventsReceived"`
	BytesReceived  int64 `json:"bytesReceived"`
	PushesSent     int64 `json:"pushesSent"`
	PushErrors     int64 `json:"pushErrors"`
	MatchedEvents  int64 `json:"matchedEvents"`
	EventsDropped  int64 `json:"eventsDropped"`
	LastCursor     int64 `json:"lastCursor"`
}
```

Update `GetStats` to return `EventsDropped: c.eventsDropped.Load()`.

Add a new type above `Consumer`:

```go
type dispatchItem struct {
	actorDID string
	commit   *CommitEvent
}
```

- [ ] **Step 2: Initialize the channel in NewConsumer**

```go
func NewConsumer(url string, s *store.Store, sender *push.MultiSender, profileResolver *profile.Resolver) *Consumer {
	c := &Consumer{
		url:             url,
		store:           s,
		sender:          sender,
		profileResolver: profileResolver,
		stopCh:          make(chan struct{}),
		startCh:         make(chan struct{}),
		commitCh:        make(chan dispatchItem, 1024),
	}
	if s.HasRegisteredDIDs() {
		close(c.startCh)
	}
	return c
}
```

- [ ] **Step 3: Start workers in Run**

In `Run`, right after the "first token registered" log line, start the worker pool:

```go
	const numWorkers = 8
	for i := 0; i < numWorkers; i++ {
		go c.dispatchWorker()
	}
```

Add the worker method just below `Run`:

```go
func (c *Consumer) dispatchWorker() {
	for {
		select {
		case <-c.stopCh:
			return
		case item, ok := <-c.commitCh:
			if !ok {
				return
			}
			c.handleCommit(item.actorDID, item.commit)
		}
	}
}
```

- [ ] **Step 4: Non-blocking enqueue in connect**

In `connect`, replace:

```go
		if event.Kind != "commit" || event.Commit == nil {
			continue
		}

		c.handleCommit(event.DID, event.Commit)
```

with:

```go
		if event.Kind != "commit" || event.Commit == nil {
			continue
		}

		select {
		case c.commitCh <- dispatchItem{actorDID: event.DID, commit: event.Commit}:
		default:
			// Queue full → drop this event rather than block the reader.
			c.eventsDropped.Add(1)
		}
```

- [ ] **Step 5: Build and test**

```bash
go build ./...
go test ./...
```

Expected: all pass. Existing consumer tests call `handleCommit` directly, which is unchanged, so they continue to work.

- [ ] **Step 6: Commit**

```bash
git add internal/jetstream/consumer.go
git commit -m "fix(jetstream): move push dispatch to bounded worker pool

Previously the websocket read loop synchronously ran handleCommit and
sendNotification (including HTTPS calls to APNs/FCM/Expo), so a single
slow upstream would stall the reader, fill TCP buffers, and eventually
cause a disconnect.

Introduces an 8-worker pool fed by a 1024-buffered channel. On a full
queue, events are dropped (counted in stats.EventsDropped) rather than
blocking the reader. For the target scale of a few dozen users this
never fires; for bursts or degraded providers, it's the difference
between a dropped notification and a full stall."
```

---

## Task 15: I14 — Backfill existing blocks on first token registration

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/xrpc/handler.go`
- Create: `internal/xrpc/blocks.go` (or add to handler.go — pick one)

**Context:** The in-memory block graph is only populated by Jetstream events seen *after* the user's token is registered. A user who previously blocked someone will still receive notifications from them until they re-block, which is a surprising correctness bug.

Fix: on first token registration for a DID, fetch the block list from the public AppView (`app.bsky.graph.getBlocks` requires auth, so instead use `com.atproto.repo.listRecords` via the user's PDS — but the gateway doesn't know the user's PDS or have auth). The pragmatic alternative is the public AppView endpoint for per-repo record listing on the user's own repo:

`GET https://public.api.bsky.app/xrpc/com.atproto.repo.listRecords?repo=<DID>&collection=app.bsky.graph.block&limit=100`

This is a public endpoint; blocks on the repo are public records (even if the AppView UI hides them). Iterate with `cursor` until exhausted, and call `store.AddBlock` for each.

Do this asynchronously (don't block `registerPush` on the backfill) and only once per DID, guarded by a new `blocks_backfilled` table.

### Steps

- [ ] **Step 1: Add a backfilled-DIDs table and check**

In `internal/store/store.go`, in the schema DDL inside `New`, add:

```sql
		CREATE TABLE IF NOT EXISTS blocks_backfilled (
			actor_did TEXT PRIMARY KEY,
			backfilled_at TEXT DEFAULT (datetime('now'))
		);
```

Add methods:

```go
// MarkBlocksBackfilled records that this DID's historical blocks have
// been fetched. Returns true if the row was newly inserted (i.e. this
// caller should perform the backfill), false if already done.
func (s *Store) MarkBlocksBackfilled(actorDID string) (bool, error) {
	res, err := s.db.Exec(
		"INSERT OR IGNORE INTO blocks_backfilled (actor_did) VALUES (?)",
		actorDID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
```

- [ ] **Step 2: Add the backfill logic in handler**

Create `internal/xrpc/blocks.go`:

```go
package xrpc

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// backfillClient is a shared HTTP client for public AppView fetches.
var backfillClient = &http.Client{Timeout: 10 * time.Second}

// backfillBlocks fetches historical block records from the public AppView for a
// given DID and seeds them into the store. Runs asynchronously; failures are
// logged and retried next time (because the blocks_backfilled table only marks
// success after the full backfill completes).
func (h *Handler) backfillBlocks(actorDID string) {
	const endpoint = "https://public.api.bsky.app/xrpc/com.atproto.repo.listRecords"
	const maxPages = 20 // safety: 100 records/page * 20 = 2000 blocks max
	cursor := ""
	total := 0
	for page := 0; page < maxPages; page++ {
		u, _ := url.Parse(endpoint)
		q := u.Query()
		q.Set("repo", actorDID)
		q.Set("collection", "app.bsky.graph.block")
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		u.RawQuery = q.Encode()

		resp, err := backfillClient.Get(u.String())
		if err != nil {
			log.Printf("[blocks-backfill] %s: %v", actorDID, err)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("[blocks-backfill] %s: HTTP %d", actorDID, resp.StatusCode)
			return
		}

		var out struct {
			Records []struct {
				URI   string `json:"uri"`
				Value struct {
					Subject string `json:"subject"`
				} `json:"value"`
			} `json:"records"`
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			log.Printf("[blocks-backfill] %s: parse: %v", actorDID, err)
			return
		}

		for _, rec := range out.Records {
			if rec.Value.Subject == "" {
				continue
			}
			// rkey is the last segment of the URI. We tolerate an empty rkey —
			// store.AddBlock accepts it.
			rkey := uriRKey(rec.URI)
			if err := h.store.AddBlock(actorDID, rec.Value.Subject, rkey); err != nil {
				log.Printf("[blocks-backfill] %s: add: %v", actorDID, err)
				continue
			}
			total++
		}

		if out.Cursor == "" {
			break
		}
		cursor = out.Cursor
	}

	if _, err := h.store.MarkBlocksBackfilled(actorDID); err != nil {
		log.Printf("[blocks-backfill] %s: mark: %v", actorDID, err)
		return
	}
	log.Printf("[blocks-backfill] %s: seeded %d existing blocks", actorDID, total)
}

// uriRKey returns the last path segment of an at:// URI, which is the rkey.
func uriRKey(uri string) string {
	for i := len(uri) - 1; i >= 0; i-- {
		if uri[i] == '/' {
			return uri[i+1:]
		}
	}
	return ""
}

// maybeStartBlocksBackfill claims the backfill for this DID via
// MarkBlocksBackfilled and runs it asynchronously if claimed. The mark is
// written before the backfill completes, so a mid-flight crash leaves the DID
// marked without a completed backfill — acceptable at this scale since blocks
// seen via Jetstream after the mark will still land via the live path.
func (h *Handler) maybeStartBlocksBackfill(actorDID string) {
	claimed, err := h.store.MarkBlocksBackfilled(actorDID)
	if err != nil {
		log.Printf("[blocks-backfill] claim error: %v", err)
		return
	}
	if !claimed {
		return
	}
	go h.backfillBlocks(actorDID)
}
```

Remove `"fmt"` from the imports (it's not used in this file).

- [ ] **Step 3: Call the backfill after registerPush succeeds**

In `internal/xrpc/handler.go`, in `handleRegisterPush`, right after the successful `RegisterToken` call (before `w.WriteHeader(200)`), add:

```go
	h.maybeStartBlocksBackfill(actorDID)
```

- [ ] **Step 4: Add a test**

Append to `internal/xrpc/handler_test.go`:

```go
func TestMaybeStartBlocksBackfill_OnlyRunsOncePerDID(t *testing.T) {
	h, _ := newTestHandler(t)

	// First call — claims and runs.
	h.maybeStartBlocksBackfill("did:plc:alice")
	// Second call — should no-op because already marked.
	h.maybeStartBlocksBackfill("did:plc:alice")

	// No assertion on the AppView call itself (it'll fail against the real
	// endpoint during tests); we're asserting the claim semantics via
	// MarkBlocksBackfilled.
	claimed, err := h.store.MarkBlocksBackfilled("did:plc:alice")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Error("expected did:plc:alice to already be marked as backfilled")
	}
}
```

- [ ] **Step 5: Build and test**

```bash
go build ./...
go test ./...
```

Expected: no failures.

- [ ] **Step 6: Commit**

```bash
git add internal/store/store.go internal/xrpc/handler.go internal/xrpc/blocks.go internal/xrpc/handler_test.go
git commit -m "feat(xrpc): backfill historical blocks on first token registration

Previously the block graph was only populated by Jetstream events
seen after registration, so users who blocked someone prior to
registering would still receive notifications from them until they
re-blocked.

On the first registerPush for a DID, asynchronously fetch the user's
existing app.bsky.graph.block records from the public AppView
(com.atproto.repo.listRecords) and seed them into the store. Tracked
per-DID via a new blocks_backfilled table so each DID is backfilled
once."
```

---

## Final Review

After all tasks are complete:

- [ ] Run the full test suite: `go test ./...`
- [ ] Run `go vet ./...` and fix any warnings
- [ ] Build the binary: `go build ./...`
- [ ] Review the commit log: `git log --oneline main..HEAD` — should show 15 commits, one per task.
- [ ] Dispatch final superpowers:code-reviewer subagent for entire implementation before merging.
