package xrpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/did"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

// DIDResolver resolves a DID to a DID document. Abstracted as an interface
// so tests can inject a fake resolver without hitting the network.
type DIDResolver interface {
	ResolveDID(did string) (*did.DIDDocument, error)
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ,omitempty"`
}

type jwtClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Lxm string `json:"lxm"`
	Exp int64  `json:"exp"`
}

type RegisterPushRequest struct {
	ServiceDID    string `json:"serviceDid"`
	Token         string `json:"token"`
	Platform      string `json:"platform"`
	AppID         string `json:"appId"`
	AgeRestricted bool   `json:"ageRestricted,omitempty"`
}

// StatsProvider returns jetstream stats for the health endpoint.
// Accepts any type — will be JSON-encoded directly.
type StatsProvider func() interface{}

type Handler struct {
	store              *store.Store
	devMode            bool
	serviceDID         string
	statsProvider      StatsProvider
	didResolver        DIDResolver
	onTokenRegistered  func()
}

func NewHandler(s *store.Store, devMode bool, serviceDID string, sp StatsProvider, onTokenRegistered func()) *Handler {
	return &Handler{store: s, devMode: devMode, serviceDID: serviceDID, statsProvider: sp, didResolver: did.NewResolver(), onTokenRegistered: onTokenRegistered}
}

func NewHandlerWithoutStats(s *store.Store, devMode bool, serviceDID string) *Handler {
	return &Handler{store: s, devMode: devMode, serviceDID: serviceDID, didResolver: did.NewResolver(), onTokenRegistered: nil}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, serviceDID string) {
	mux.HandleFunc("POST /xrpc/app.bsky.notification.registerPush", h.handleRegisterPush)
	mux.HandleFunc("POST /xrpc/app.bsky.notification.unregisterPush", h.handleUnregisterPush)
	mux.HandleFunc("GET /xrpc/app.bsky.notification.registerPush", methodNotAllowed)
	mux.HandleFunc("GET /xrpc/app.bsky.notification.unregisterPush", methodNotAllowed)

	// DID Document
	mux.HandleFunc("GET /.well-known/did.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"@context": []string{"https://www.w3.org/ns/did/v1"},
			"id":       serviceDID,
			"service": []map[string]string{
				{
					"id":              "#bsky_notif",
					"type":            "BskyNotificationService",
					"serviceEndpoint": "https://" + serviceDID[8:], // strip "did:web:"
				},
			},
		})
	})

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		tokens, blocks, dids := h.store.GetStats()
		result := map[string]interface{}{
			"status":         "ok",
			"registeredDIDs": dids,
			"totalTokens":    tokens,
			"trackedBlocks":  blocks,
		}
		if h.statsProvider != nil {
			result["jetstream"] = h.statsProvider()
		}
		json.NewEncoder(w).Encode(result)
	})

	// Test endpoint (dev mode only)
	if h.devMode {
		mux.HandleFunc("POST /test/register", h.handleTestRegister)
		mux.HandleFunc("POST /test/push", h.handleTestPush)
	}
}

func (h *Handler) handleRegisterPush(w http.ResponseWriter, r *http.Request) {
	var req RegisterPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request","message":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if req.ServiceDID == "" || req.Token == "" || req.Platform == "" || req.AppID == "" {
		http.Error(w, `{"error":"invalid_request","message":"missing required fields"}`, http.StatusBadRequest)
		return
	}

	if req.Platform != "ios" && req.Platform != "android" && req.Platform != "web" {
		http.Error(w, `{"error":"invalid_request","message":"invalid platform"}`, http.StatusBadRequest)
		return
	}

	// Verify inter-service JWT
	actorDID, err := h.verifyAuth(r)
	if err != nil {
		log.Printf("[xrpc] auth error: %v", err)
		http.Error(w, `{"error":"auth_required","message":"invalid service auth"}`, http.StatusUnauthorized)
		return
	}

	if err := h.store.RegisterToken(actorDID, req.Platform, req.Token, req.AppID); err != nil {
		log.Printf("[xrpc] register error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[xrpc] registered token for %s (%s/%s)", actorDID, req.Platform, req.AppID)
	if h.onTokenRegistered != nil {
		h.onTokenRegistered()
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleUnregisterPush(w http.ResponseWriter, r *http.Request) {
	var req RegisterPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	if req.ServiceDID == "" || req.Token == "" || req.Platform == "" || req.AppID == "" {
		http.Error(w, `{"error":"invalid_request","message":"missing required fields"}`, http.StatusBadRequest)
		return
	}

	actorDID, err := h.verifyAuth(r)
	if err != nil {
		http.Error(w, `{"error":"auth_required"}`, http.StatusUnauthorized)
		return
	}

	if err := h.store.UnregisterToken(actorDID, req.Platform, req.Token, req.AppID); err != nil {
		log.Printf("[xrpc] unregister error: %v", err)
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[xrpc] unregistered token for %s (%s/%s)", actorDID, req.Platform, req.AppID)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) verifyAuth(r *http.Request) (string, error) {
	if h.devMode {
		// In dev mode, accept a simple X-Actor-DID header for testing
		did := r.Header.Get("X-Actor-DID")
		if did != "" {
			return did, nil
		}
	}

	// Extract Bearer token from Authorization header
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("missing or invalid Authorization header")
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")

	// Decode JWT claims (header.payload.signature)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed JWT: expected 3 parts, got %d", len(parts))
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	// Check expiry
	if claims.Exp == 0 {
		return "", fmt.Errorf("JWT missing exp claim")
	}
	now := time.Now().Unix()
	if now > claims.Exp {
		return "", fmt.Errorf("JWT expired")
	}
	const maxLifetimeSeconds = 300 // 5 minutes
	if claims.Exp-now > maxLifetimeSeconds {
		return "", fmt.Errorf("JWT exp too far in future (%ds > %ds)", claims.Exp-now, maxLifetimeSeconds)
	}

	// Check issuer is present and looks like a DID
	if claims.Iss == "" {
		return "", fmt.Errorf("JWT missing iss claim")
	}
	if !strings.HasPrefix(claims.Iss, "did:") {
		return "", fmt.Errorf("JWT iss is not a DID: %s", claims.Iss)
	}

	if claims.Aud != h.serviceDID {
		return "", fmt.Errorf("JWT aud mismatch: got %q, want %q", claims.Aud, h.serviceDID)
	}

	// DID-based signature verification
	// 1. Decode header to check algorithm
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT header: %w", err)
	}

	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "", fmt.Errorf("failed to parse JWT header: %w", err)
	}

	// Enforce explicit algorithm allow-list. ATProto uses ES256/ES256K only.
	// Rejects "none", "HS256", "RS256", etc. before any key material is loaded.
	if header.Alg != "ES256" && header.Alg != "ES256K" {
		return "", fmt.Errorf("unsupported JWT algorithm: %q", header.Alg)
	}

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
}

// verifyECDSASignature verifies an ECDSA signature in the JWS format (r || s concatenation).
func verifyECDSASignature(pubKey *ecdsa.PublicKey, hash []byte, sig []byte) bool {
	keySize := (pubKey.Curve.Params().BitSize + 7) / 8

	// JWS ECDSA signatures are r || s, each padded to key size
	if len(sig) != 2*keySize {
		// Try ASN.1 DER format as fallback
		return ecdsa.VerifyASN1(pubKey, hash, sig)
	}

	r := new(big.Int).SetBytes(sig[:keySize])
	s := new(big.Int).SetBytes(sig[keySize:])

	return ecdsa.Verify(pubKey, hash, r, s)
}

// Dev mode: register without JWT
func (h *Handler) handleTestRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActorDID string `json:"actorDid"`
		Token    string `json:"token"`
		Platform string `json:"platform"`
		AppID    string `json:"appId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	if err := h.store.RegisterToken(req.ActorDID, req.Platform, req.Token, req.AppID); err != nil {
		http.Error(w, `{"error":"internal_error"}`, http.StatusInternalServerError)
		return
	}

	log.Printf("[test] registered token for %s", req.ActorDID)
	if h.onTokenRegistered != nil {
		h.onTokenRegistered()
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "registered"})
}

// Dev mode: trigger a test push
func (h *Handler) handleTestPush(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ActorDID string `json:"actorDid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}

	tokens, err := h.store.GetTokensForDID(req.ActorDID)
	if err != nil || len(tokens) == 0 {
		http.Error(w, `{"error":"no_tokens"}`, http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "found",
		"tokens": len(tokens),
	})
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
}
