// Package relay implements a Web Push relay (RFC 8030) for Fediverse
// servers: Mastodon and anything speaking its client API — Pleroma/Akkoma,
// GoToSocial, Iceshrimp, Pixelfed.
//
// Unlike the ATproto side of this gateway, which watches a firehose and
// composes notifications itself, the relay never learns what it is carrying.
// A Fediverse server encrypts a notification for the device's public key and
// POSTs the ciphertext here; the relay looks up which device the endpoint
// belongs to, base64s the blob into a push payload, and forwards it. The
// private key never leaves the device, so only the device can read it.
//
// That matters beyond general hygiene: a Mastodon web push payload contains
// the user's OAuth access token (see Web::NotificationSerializer upstream).
// A relay that decrypted would hold account-level credentials for every
// user it serves. This one cannot.
//
// # Status codes are a security control here
//
// Mastodon's Web::PushNotificationWorker destroys the subscription on any
// 4xx except 408 and 429:
//
//	if (400..499).cover?(response.code) && ![408, 429].include?(response.code)
//	  @subscription.destroy!
//
// There is no retry. So a 4xx is not "this request failed", it is "cancel
// this subscription forever" — and the user silently stops receiving
// notifications until they re-register.
//
// The consequence is that rejections must NOT be expressed as 4xx. If a
// failed VAPID check returned 403, anyone who learned an endpoint URL could
// destroy that user's subscription with a single forged request: a worse
// outcome than the spam the check exists to prevent. Rejected pushes are
// therefore accepted and dropped. 4xx is reserved for the one case where
// cancellation is the correct outcome: the endpoint is genuinely gone.
package relay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
	"github.com/dracoblue/atproto-push-gateway/internal/vapid"
)

// maxCiphertextBytes caps what the relay will accept into a push payload.
//
// APNs allows 4KB total; the ciphertext is base64'd (+33%) and shares the
// payload with the aps block, so the usable budget is roughly 2.8KB of
// ciphertext. Mastodon truncates the notification body to 140 characters,
// which in practice lands well under 1KB — so this ceiling should never be
// reached. The handler logs every ciphertext length precisely so the real
// distribution can be checked against this assumption in production.
const maxCiphertextBytes = 2800

// readLimit is how much the relay reads before giving up, set above
// maxCiphertextBytes so oversized bodies are detected rather than truncated
// into something that looks valid.
const readLimit = 64 * 1024

// placeholderTitle is shown only if the device fails to decrypt and rewrite
// the notification.
//
// iOS requires a non-empty alert plus mutable-content to launch the
// Notification Service Extension at all, so something user-visible has to be
// sent. A UNNotificationServiceExtension cannot suppress a notification once
// delivered — it may replace the content, not cancel it — which means a push
// the device cannot decrypt surfaces as this bare title. Keep it neutral and
// unalarming, and keep the relay-side checks strict so it stays rare.
const placeholderTitle = "Fediverse"

// Store is the subset of the gateway store the relay needs.
type Store interface {
	CreateWebPushEndpoint(e store.WebPushEndpoint) (string, error)
	GetWebPushEndpoint(id string) (store.WebPushEndpoint, error)
	DeleteWebPushEndpoint(id string) error
	DeleteWebPushEndpointsForDevice(deviceToken, appID string) error
	TouchWebPushEndpoint(id string) error
}

// Handler serves the relay endpoints.
type Handler struct {
	store   Store
	sender  push.Sender
	baseURL string // public origin, e.g. "https://push.kiesel.app"

	limiter *rateLimiter
}

// NewHandler builds a relay handler. baseURL is the publicly reachable
// origin of this gateway; it is handed to clients as the endpoint prefix
// they pass to their instance, so it must be what the instance can resolve.
func NewHandler(s Store, sender push.Sender, baseURL string) *Handler {
	return &Handler{
		store:   s,
		sender:  sender,
		baseURL: strings.TrimRight(baseURL, "/"),
		limiter: newRateLimiter(),
	}
}

// RegisterRoutes wires the relay into a mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /relay/register", h.handleRegister)
	mux.HandleFunc("POST /relay/unregister", h.handleUnregister)
	mux.HandleFunc("POST /relay/{id}", h.handlePush)
	// Some senders probe an endpoint before using it; answering 405 rather
	// than 404 keeps the subscription alive (404 would destroy it).
	mux.HandleFunc("GET /relay/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	})
}

type registerRequest struct {
	// DeviceToken is the native APNs or FCM token.
	DeviceToken string `json:"deviceToken"`
	Platform    string `json:"platform"` // "ios" | "android"
	AppID       string `json:"appId"`
	// VAPIDPublicKey is the instance's own VAPID key, which the client reads
	// from GET /api/v2/instance → configuration.vapid.public_key. Pinning it
	// is what restricts this endpoint to that one instance. Empty disables
	// the check — see handlePush.
	VAPIDPublicKey string `json:"vapidPublicKey"`
	// Instance is the host the subscription belongs to, kept for operator
	// diagnostics only. It is never used for authorization.
	Instance string `json:"instance"`
}

type registerResponse struct {
	// Endpoint is what the client passes to its instance as
	// subscription[endpoint].
	Endpoint string `json:"endpoint"`
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, readLimit)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	if req.Platform != "ios" && req.Platform != "android" {
		http.Error(w, "platform must be \"ios\" or \"android\"", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.DeviceToken) == "" {
		http.Error(w, "deviceToken is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.AppID) == "" {
		http.Error(w, "appId is required", http.StatusBadRequest)
		return
	}

	id, err := h.store.CreateWebPushEndpoint(store.WebPushEndpoint{
		Platform:       req.Platform,
		DeviceToken:    strings.TrimSpace(req.DeviceToken),
		AppID:          strings.TrimSpace(req.AppID),
		VAPIDPublicKey: strings.TrimSpace(req.VAPIDPublicKey),
		Instance:       strings.TrimSpace(req.Instance),
	})
	if err != nil {
		log.Printf("[relay] register failed: %v", err)
		http.Error(w, "failed to create endpoint", http.StatusInternalServerError)
		return
	}

	pinned := "pinned"
	if req.VAPIDPublicKey == "" {
		pinned = "UNPINNED (any sender who learns the URL can push)"
	}
	log.Printf("[relay] registered endpoint for %s/%s instance=%q vapid=%s",
		req.Platform, truncateToken(req.DeviceToken), req.Instance, pinned)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(registerResponse{
		Endpoint: fmt.Sprintf("%s/relay/%s", h.baseURL, id),
	})
}

type unregisterRequest struct {
	// Endpoint is the full URL handed out by register; the id is taken from
	// its last path segment so a client can pass back what it stored.
	Endpoint string `json:"endpoint"`
	// Alternatively a client may clear everything for its device token,
	// which is what a logout path wants.
	DeviceToken string `json:"deviceToken"`
	AppID       string `json:"appId"`
}

func (h *Handler) handleUnregister(w http.ResponseWriter, r *http.Request) {
	var req unregisterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, readLimit)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	switch {
	case strings.TrimSpace(req.Endpoint) != "":
		id := req.Endpoint
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if err := h.store.DeleteWebPushEndpoint(id); err != nil {
			log.Printf("[relay] unregister failed: %v", err)
			http.Error(w, "failed to remove endpoint", http.StatusInternalServerError)
			return
		}
	case strings.TrimSpace(req.DeviceToken) != "" && strings.TrimSpace(req.AppID) != "":
		if err := h.store.DeleteWebPushEndpointsForDevice(req.DeviceToken, req.AppID); err != nil {
			log.Printf("[relay] unregister by device failed: %v", err)
			http.Error(w, "failed to remove endpoints", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "need endpoint, or deviceToken plus appId", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePush is the endpoint a Fediverse server delivers to.
//
// Read the status codes against the package comment: every non-delivery path
// below deliberately avoids 4xx, because 4xx makes Mastodon destroy the
// subscription. The only 4xx is the unknown-endpoint case, where that is the
// outcome we want.
func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	endpoint, err := h.store.GetWebPushEndpoint(id)
	if err != nil {
		if errors.Is(err, store.ErrEndpointNotFound) {
			// The one legitimate 4xx. The endpoint really is gone, so the
			// sender should stop and drop the subscription. RFC 8030 uses
			// 404/410 for exactly this.
			log.Printf("[relay] unknown endpoint, telling sender to drop subscription")
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		// Infrastructure problem — 5xx so the sender retries later rather
		// than cancelling a subscription that is actually fine.
		log.Printf("[relay] endpoint lookup failed: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Rate limit before signature work so a flood costs us little. 429 is in
	// Mastodon's exemption list, so it retries instead of unsubscribing.
	if !h.limiter.allow(id) {
		log.Printf("[relay] rate limited endpoint")
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}

	// VAPID. A mismatch means someone other than the registered instance is
	// pushing here. Accept and drop: signalling the rejection with a 4xx
	// would hand that someone the power to kill the subscription.
	if err := vapid.Verify(r.Header.Get("Authorization"), endpoint.VAPIDPublicKey); err != nil {
		if endpoint.VAPIDPublicKey == "" {
			log.Printf("[relay] unpinned endpoint accepted a push with bad VAPID (%v) — "+
				"re-register with vapidPublicKey to close this", err)
		} else {
			log.Printf("[relay] dropping push with failed VAPID check: %v", err)
			h.accepted(w)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, readLimit))
	if err != nil {
		log.Printf("[relay] failed to read body: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Always log the exact length: this is the measurement that tells us
	// whether real-world payloads stay inside the APNs budget.
	encoding := r.Header.Get("Content-Encoding")
	log.Printf("[relay] push received: %d bytes ciphertext, Content-Encoding=%q, platform=%s",
		len(body), encoding, endpoint.Platform)

	if len(body) == 0 {
		log.Printf("[relay] dropping empty push body")
		h.accepted(w)
		return
	}
	if len(body) > maxCiphertextBytes {
		// Nothing useful can be forwarded, and the device could not render it
		// anyway. Dropping silently keeps the subscription alive so the next,
		// normally-sized notification still arrives.
		log.Printf("[relay] dropping oversized push: %d bytes exceeds %d",
			len(body), maxCiphertextBytes)
		h.accepted(w)
		return
	}
	if encoding != "" && encoding != "aes128gcm" {
		// Legacy aesgcm carries its salt and server key in separate headers
		// rather than in the body, so forwarding the body alone would give
		// the device something it cannot decrypt. Clients should subscribe
		// with subscription[standard]=true to get aes128gcm.
		log.Printf("[relay] dropping push with unsupported Content-Encoding %q "+
			"(client should subscribe with standard=true)", encoding)
		h.accepted(w)
		return
	}

	n := push.Notification{
		Token:    endpoint.DeviceToken,
		Platform: endpoint.Platform,
		Title:    placeholderTitle,
		Data: map[string]string{
			// "m" carries the ciphertext; the device decrypts it in its NSE
			// (iOS) or FirebaseMessagingService (Android) and replaces the
			// placeholder content.
			"m":      base64.RawURLEncoding.EncodeToString(body),
			"source": "webpush",
		},
		// Android must be data-only regardless of the gateway's global
		// FCM_DATA_ONLY setting: a notification message would be rendered by
		// the OS as the bare placeholder without ever waking the client that
		// holds the decryption key.
		DataOnly: true,
	}

	if err := h.sender.Send(n); err != nil {
		if errors.Is(err, push.ErrTokenInvalid) {
			// The device is gone for good. Drop our mapping, then report 404
			// so the instance also stops pushing — the subscription genuinely
			// has no recipient any more.
			log.Printf("[relay] device token permanently invalid, removing endpoint: %v", err)
			if derr := h.store.DeleteWebPushEndpointsForDevice(endpoint.DeviceToken, endpoint.AppID); derr != nil {
				log.Printf("[relay] failed to remove endpoint for dead token: %v", derr)
			}
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		// Transient delivery failure — 5xx so Mastodon retries (retry: 5).
		log.Printf("[relay] delivery failed: %v", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	if err := h.store.TouchWebPushEndpoint(id); err != nil {
		log.Printf("[relay] failed to record push timestamp: %v", err)
	}

	w.WriteHeader(http.StatusCreated)
}

// accepted answers a dropped-but-not-rejected push. 202 tells the sender the
// delivery was taken off its hands, which keeps the subscription alive.
func (h *Handler) accepted(w http.ResponseWriter) {
	w.WriteHeader(http.StatusAccepted)
}

func truncateToken(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12] + "…"
}

// rateLimiter is a small fixed-window counter per endpoint id.
//
// A Fediverse account under a notification storm can legitimately produce a
// burst, so the window is generous; this exists to bound abuse of a leaked
// endpoint URL, not to shape normal traffic.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
}

type window struct {
	count int
	start time.Time
}

const (
	rateWindow = time.Minute
	rateLimit  = 60
)

func newRateLimiter() *rateLimiter {
	return &rateLimiter{windows: make(map[string]*window)}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) > rateWindow {
		// Opportunistically drop expired windows so the map cannot grow
		// without bound as endpoints come and go.
		if len(l.windows) > 10000 {
			for k, v := range l.windows {
				if now.Sub(v.start) > rateWindow {
					delete(l.windows, k)
				}
			}
		}
		l.windows[key] = &window{count: 1, start: now}
		return true
	}

	if w.count >= rateLimit {
		return false
	}
	w.count++
	return true
}
