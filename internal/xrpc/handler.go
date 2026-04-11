package xrpc

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

type RegisterPushRequest struct {
	ServiceDID    string `json:"serviceDid"`
	Token         string `json:"token"`
	Platform      string `json:"platform"`
	AppID         string `json:"appId"`
	AgeRestricted bool   `json:"ageRestricted,omitempty"`
}

type Handler struct {
	store   *store.Store
	devMode bool
}

func NewHandler(s *store.Store, devMode bool) *Handler {
	return &Handler{store: s, devMode: devMode}
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
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":          "ok",
			"registeredDIDs":  dids,
			"totalTokens":     tokens,
			"trackedBlocks":   blocks,
		})
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

	// TODO: Full inter-service JWT verification
	// 1. Extract Bearer token from Authorization header
	// 2. Decode JWT header + claims
	// 3. Resolve iss DID → get public key from DID document
	// 4. Verify Schnorr/ES256K signature
	// 5. Check: aud matches our service DID, lxm matches method, exp valid
	// 6. Optional: jti replay protection

	return "", http.ErrAbortHandler
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
