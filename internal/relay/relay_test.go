package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

// fakeStore is an in-memory stand-in holding a single endpoint.
type fakeStore struct {
	endpoints map[string]store.WebPushEndpoint
	created   string
	deleted   []string
	touched   []string
	createErr error
	getErr    error
}

func newFakeStore() *fakeStore {
	return &fakeStore{endpoints: make(map[string]store.WebPushEndpoint)}
}

func (f *fakeStore) CreateWebPushEndpoint(e store.WebPushEndpoint) (string, error) {
	if f.createErr != nil {
		return "", f.createErr
	}
	id := "endpoint-id-" + fmt.Sprint(len(f.endpoints))
	f.endpoints[id] = e
	f.created = id
	return id, nil
}

func (f *fakeStore) GetWebPushEndpoint(id string) (store.WebPushEndpoint, error) {
	if f.getErr != nil {
		return store.WebPushEndpoint{}, f.getErr
	}
	e, ok := f.endpoints[id]
	if !ok {
		return store.WebPushEndpoint{}, store.ErrEndpointNotFound
	}
	return e, nil
}

func (f *fakeStore) DeleteWebPushEndpoint(id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.endpoints, id)
	return nil
}

func (f *fakeStore) DeleteWebPushEndpointsForDevice(deviceToken, appID string) error {
	f.deleted = append(f.deleted, deviceToken)
	for id, e := range f.endpoints {
		if e.DeviceToken == deviceToken && e.AppID == appID {
			delete(f.endpoints, id)
		}
	}
	return nil
}

func (f *fakeStore) TouchWebPushEndpoint(id string) error {
	f.touched = append(f.touched, id)
	return nil
}

// fakeSender records what was handed to the push layer.
type fakeSender struct {
	sent []push.Notification
	err  error
}

func (s *fakeSender) Send(n push.Notification) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, n)
	return nil
}

func vapidKeyPair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	raw := make([]byte, 65)
	raw[0] = 4
	key.PublicKey.X.FillBytes(raw[1:33])
	key.PublicKey.Y.FillBytes(raw[33:65])
	return key, base64.RawURLEncoding.EncodeToString(raw)
}

func vapidHeader(t *testing.T, key *ecdsa.PrivateKey, pub string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}
	return "vapid t=" + signed + ",k=" + pub
}

// setup builds a handler with one registered endpoint whose VAPID key is
// pinned, and returns everything a push test needs.
func setup(t *testing.T) (*http.ServeMux, *fakeStore, *fakeSender, string, string) {
	t.Helper()
	key, pub := vapidKeyPair(t)
	st := newFakeStore()
	st.endpoints["known"] = store.WebPushEndpoint{
		Platform:       "ios",
		DeviceToken:    "device-token-abc",
		AppID:          "app.kiesel.Kiesel",
		VAPIDPublicKey: pub,
		Instance:       "mastodon.social",
	}
	sender := &fakeSender{}
	mux := http.NewServeMux()
	NewHandler(st, sender, "https://push.example.org").RegisterRoutes(mux)
	return mux, st, sender, vapidHeader(t, key, pub), pub
}

func pushTo(mux *http.ServeMux, id, auth string, body []byte, encoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/relay/"+id, bytes.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPush_ValidDeliversCiphertext(t *testing.T) {
	mux, st, sender, auth, _ := setup(t)
	ciphertext := []byte("encrypted-notification-bytes")

	rec := pushTo(mux, "known", auth, ciphertext, "aes128gcm")

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected 1 push, got %d", len(sender.sent))
	}

	n := sender.sent[0]
	if n.Token != "device-token-abc" {
		t.Errorf("wrong device token: %q", n.Token)
	}
	if !n.DataOnly {
		t.Error("relay pushes must be data-only so the client can decrypt")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(n.Data["m"])
	if err != nil {
		t.Fatalf("data.m is not valid base64url: %v", err)
	}
	if !bytes.Equal(decoded, ciphertext) {
		t.Errorf("ciphertext round-trip failed: got %q", decoded)
	}
	if len(st.touched) != 1 {
		t.Errorf("expected endpoint to be touched once, got %d", len(st.touched))
	}
}

// The core policy test. Mastodon destroys a subscription on any 4xx except
// 408/429, so a forged push must never produce one — otherwise knowing the
// URL is enough to permanently kill a user's notifications.
func TestPush_FailedVAPIDIsAcceptedNotRejected(t *testing.T) {
	mux, _, sender, _, _ := setup(t)
	attackerKey, attackerPub := vapidKeyPair(t)
	forged := vapidHeader(t, attackerKey, attackerPub)

	rec := pushTo(mux, "known", forged, []byte("whatever"), "aes128gcm")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("forged VAPID must yield 202 (never 4xx, which would destroy "+
			"the subscription), got %d", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Errorf("forged push must not be delivered, got %d", len(sender.sent))
	}
}

func TestPush_MissingVAPIDIsAcceptedNotRejected(t *testing.T) {
	mux, _, sender, _, _ := setup(t)

	rec := pushTo(mux, "known", "", []byte("whatever"), "aes128gcm")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("push without VAPID must not be delivered")
	}
}

// The one case where 4xx is correct: the endpoint really is gone, so the
// sender should stop and drop the subscription.
func TestPush_UnknownEndpointReturns404(t *testing.T) {
	mux, _, _, auth, _ := setup(t)

	rec := pushTo(mux, "does-not-exist", auth, []byte("x"), "aes128gcm")

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown endpoint, got %d", rec.Code)
	}
}

func TestPush_OversizedIsAcceptedAndDropped(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)
	huge := bytes.Repeat([]byte("A"), maxCiphertextBytes+1)

	rec := pushTo(mux, "known", auth, huge, "aes128gcm")

	if rec.Code != http.StatusAccepted {
		t.Errorf("oversized payload must yield 202 so the subscription "+
			"survives for the next normal notification, got %d", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("oversized payload must not be delivered")
	}
}

func TestPush_AtSizeLimitIsDelivered(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)
	atLimit := bytes.Repeat([]byte("A"), maxCiphertextBytes)

	rec := pushTo(mux, "known", auth, atLimit, "aes128gcm")

	if rec.Code != http.StatusCreated {
		t.Errorf("payload exactly at the limit should be delivered, got %d", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Errorf("expected delivery, got %d pushes", len(sender.sent))
	}
}

func TestPush_EmptyBodyIsAcceptedAndDropped(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)

	rec := pushTo(mux, "known", auth, nil, "aes128gcm")

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("empty body must not be delivered")
	}
}

// Legacy aesgcm keeps salt and server key in separate headers, so the body
// alone is undecryptable. Drop it without killing the subscription.
func TestPush_LegacyEncodingIsAcceptedAndDropped(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)

	rec := pushTo(mux, "known", auth, []byte("legacy-ciphertext"), "aesgcm")

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected 202 for legacy encoding, got %d", rec.Code)
	}
	if len(sender.sent) != 0 {
		t.Error("legacy-encoded push must not be delivered")
	}
}

// No Content-Encoding at all is tolerated: some senders omit it and the body
// is aes128gcm in practice.
func TestPush_MissingEncodingIsDelivered(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)

	rec := pushTo(mux, "known", auth, []byte("ciphertext"), "")

	if rec.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Error("expected delivery")
	}
}

// A transient delivery failure must be 5xx so the sender retries, not 4xx.
func TestPush_DeliveryFailureReturns5xx(t *testing.T) {
	mux, _, sender, auth, _ := setup(t)
	sender.err = fmt.Errorf("APNs returned 503")

	rec := pushTo(mux, "known", auth, []byte("ciphertext"), "aes128gcm")

	if rec.Code < 500 {
		t.Errorf("transient failure must be 5xx so the sender retries, got %d", rec.Code)
	}
}

// A permanently dead device token is the other case where cancelling is
// right: there is no recipient any more.
func TestPush_DeadTokenRemovesEndpointAndReturns404(t *testing.T) {
	mux, st, sender, auth, _ := setup(t)
	sender.err = fmt.Errorf("%w: APNs 410 Unregistered", push.ErrTokenInvalid)

	rec := pushTo(mux, "known", auth, []byte("ciphertext"), "aes128gcm")

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for dead token, got %d", rec.Code)
	}
	if len(st.deleted) == 0 {
		t.Error("expected the endpoint to be removed for a dead token")
	}
}

// A store outage must not look like a dead subscription.
func TestPush_StoreErrorReturns5xx(t *testing.T) {
	mux, st, _, auth, _ := setup(t)
	st.getErr = fmt.Errorf("database is locked")

	rec := pushTo(mux, "known", auth, []byte("x"), "aes128gcm")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("store failure must be 5xx, not a cancellation, got %d", rec.Code)
	}
}

// 429 is in Mastodon's exemption list, so rate limiting is safe to signal.
func TestPush_RateLimitReturns429(t *testing.T) {
	mux, _, _, auth, _ := setup(t)

	var lastCode int
	for i := 0; i < rateLimit+5; i++ {
		lastCode = pushTo(mux, "known", auth, []byte("ciphertext"), "aes128gcm").Code
	}

	if lastCode != http.StatusTooManyRequests {
		t.Errorf("expected 429 once the window is exhausted, got %d", lastCode)
	}
}

// GET must not 404: some senders probe before using an endpoint, and a 404
// would destroy the subscription.
func TestPush_GetReturns405NotFound(t *testing.T) {
	mux, _, _, _, _ := setup(t)

	req := httptest.NewRequest("GET", "/relay/known", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

// An unpinned endpoint is the bring-up mode: it accepts pushes even with a
// bad VAPID header, so a deployment can be measured before keys are wired.
func TestPush_UnpinnedEndpointAcceptsForeignKey(t *testing.T) {
	st := newFakeStore()
	st.endpoints["unpinned"] = store.WebPushEndpoint{
		Platform:    "android",
		DeviceToken: "fcm-token",
		AppID:       "app.kiesel.Kiesel",
		// no VAPIDPublicKey
	}
	sender := &fakeSender{}
	mux := http.NewServeMux()
	NewHandler(st, sender, "https://push.example.org").RegisterRoutes(mux)

	attackerKey, attackerPub := vapidKeyPair(t)
	rec := pushTo(mux, "unpinned", vapidHeader(t, attackerKey, attackerPub), []byte("ct"), "aes128gcm")

	if rec.Code != http.StatusCreated {
		t.Errorf("expected unpinned endpoint to deliver, got %d", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Error("expected delivery on unpinned endpoint")
	}
}

func TestRegister_ReturnsEndpointURL(t *testing.T) {
	st := newFakeStore()
	mux := http.NewServeMux()
	NewHandler(st, &fakeSender{}, "https://push.example.org/").RegisterRoutes(mux)

	body, _ := json.Marshal(registerRequest{
		DeviceToken:    "device-abc",
		Platform:       "ios",
		AppID:          "app.kiesel.Kiesel",
		VAPIDPublicKey: "BCk-key",
		Instance:       "mastodon.social",
	})
	req := httptest.NewRequest("POST", "/relay/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp registerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	// Trailing slash on the base URL must not produce a double slash.
	if !strings.HasPrefix(resp.Endpoint, "https://push.example.org/relay/") {
		t.Errorf("unexpected endpoint URL: %q", resp.Endpoint)
	}
	if strings.Contains(strings.TrimPrefix(resp.Endpoint, "https://"), "//") {
		t.Errorf("endpoint URL has a double slash: %q", resp.Endpoint)
	}
}

func TestRegister_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		body registerRequest
	}{
		{"missing platform", registerRequest{DeviceToken: "t", AppID: "a"}},
		{"bad platform", registerRequest{DeviceToken: "t", AppID: "a", Platform: "windows"}},
		{"missing device token", registerRequest{Platform: "ios", AppID: "a"}},
		{"missing app id", registerRequest{Platform: "ios", DeviceToken: "t"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			NewHandler(newFakeStore(), &fakeSender{}, "https://push.example.org").RegisterRoutes(mux)

			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("POST", "/relay/register", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d", rec.Code)
			}
		})
	}
}

func TestRegister_NormalizesPlatformCase(t *testing.T) {
	st := newFakeStore()
	mux := http.NewServeMux()
	NewHandler(st, &fakeSender{}, "https://push.example.org").RegisterRoutes(mux)

	body, _ := json.Marshal(registerRequest{
		DeviceToken: "t", AppID: "a", Platform: "iOS",
	})
	req := httptest.NewRequest("POST", "/relay/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}
	if got := st.endpoints[st.created].Platform; got != "ios" {
		t.Errorf("platform not normalized: %q", got)
	}
}

func TestUnregister_ByEndpointURL(t *testing.T) {
	mux, st, _, _, _ := setup(t)

	body, _ := json.Marshal(unregisterRequest{
		Endpoint: "https://push.example.org/relay/known",
	})
	req := httptest.NewRequest("POST", "/relay/unregister", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(st.deleted) != 1 || st.deleted[0] != "known" {
		t.Errorf("expected endpoint id to be extracted from the URL, got %v", st.deleted)
	}
}

func TestUnregister_ByDeviceToken(t *testing.T) {
	mux, st, _, _, _ := setup(t)

	body, _ := json.Marshal(unregisterRequest{
		DeviceToken: "device-token-abc",
		AppID:       "app.kiesel.Kiesel",
	})
	req := httptest.NewRequest("POST", "/relay/unregister", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if len(st.endpoints) != 0 {
		t.Error("expected the device's endpoints to be gone")
	}
}

func TestUnregister_RequiresIdentifier(t *testing.T) {
	mux, _, _, _, _ := setup(t)

	req := httptest.NewRequest("POST", "/relay/unregister", bytes.NewReader([]byte("{}")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
