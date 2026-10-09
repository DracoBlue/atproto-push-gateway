package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

// These tests drive the handler against the real store rather than the
// fakeStore used elsewhere in this package. The unit tests cover policy
// decisions; these cover the wiring between them — that an endpoint URL
// handed out by register actually resolves on the push path, which no unit
// test could show because each half was exercised in isolation.

type recordingSender struct {
	sent []push.Notification
	err  error
}

func (s *recordingSender) Send(n push.Notification) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, n)
	return nil
}

func realStoreHandler(t *testing.T, sender push.Sender) (*http.ServeMux, *store.Store) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mux := http.NewServeMux()
	NewHandler(st, sender, "https://push.example.org").RegisterRoutes(mux)
	return mux, st
}

func registerVia(t *testing.T, mux *http.ServeMux, deviceToken string) string {
	t.Helper()
	body, _ := json.Marshal(registerRequest{
		DeviceToken: deviceToken,
		Platform:    "ios",
		AppID:       "app.test",
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/relay/register", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("register returned %d: %s", rec.Code, rec.Body.String())
	}
	var resp registerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid register response: %v", err)
	}
	return resp.Endpoint
}

func pushToURL(mux *http.ServeMux, url string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "aes128gcm")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The endpoint URL register hands out must be usable as-is. The id travels
// through base64url encoding, an http route pattern and a SHA-256 lookup, so
// a mismatch anywhere in that chain would surface as a 404 that looks like an
// expired subscription.
func TestIntegration_RegisteredEndpointAcceptsPush(t *testing.T) {
	sender := &recordingSender{}
	mux, _ := realStoreHandler(t, sender)

	endpoint := registerVia(t, mux, "device-abc")
	rec := pushToURL(mux, endpoint, []byte("ciphertext"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("pushing to a freshly registered endpoint returned %d, want 201", rec.Code)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("expected the push to be delivered, got %d", len(sender.sent))
	}
}

// A dead device token removes the endpoint, so the instance is told to stop.
// Observed in production with a dummy token: APNs answers 400 BadDeviceToken,
// which apns.go maps to ErrTokenInvalid.
func TestIntegration_DeadTokenRemovesEndpointForGood(t *testing.T) {
	sender := &recordingSender{err: fmt.Errorf("%w: APNs 400 BadDeviceToken", push.ErrTokenInvalid)}
	mux, st := realStoreHandler(t, sender)

	endpoint := registerVia(t, mux, "dummy-token")

	if rec := pushToURL(mux, endpoint, []byte("ciphertext")); rec.Code != http.StatusNotFound {
		t.Fatalf("first push should report 404 for a dead token, got %d", rec.Code)
	}

	// The endpoint is gone, so a second push cannot resolve it either.
	if rec := pushToURL(mux, endpoint, []byte("ciphertext")); rec.Code != http.StatusNotFound {
		t.Errorf("second push should still be 404, got %d", rec.Code)
	}
	if got := st.CountWebPushEndpoints(); got != 0 {
		t.Errorf("expected the endpoint to be removed, %d left", got)
	}
}

// Re-registering a device must invalidate the URL the instance still holds,
// otherwise a stale subscription keeps delivering to a replaced endpoint.
func TestIntegration_ReregisterInvalidatesOldEndpoint(t *testing.T) {
	sender := &recordingSender{}
	mux, _ := realStoreHandler(t, sender)

	first := registerVia(t, mux, "device-abc")
	second := registerVia(t, mux, "device-abc")
	if first == second {
		t.Fatal("re-registering must hand out a different endpoint URL")
	}

	if rec := pushToURL(mux, first, []byte("ciphertext")); rec.Code != http.StatusNotFound {
		t.Errorf("the superseded endpoint should be 404, got %d", rec.Code)
	}
	if rec := pushToURL(mux, second, []byte("ciphertext")); rec.Code != http.StatusCreated {
		t.Errorf("the current endpoint should accept pushes, got %d", rec.Code)
	}
}

func TestIntegration_UnregisterStopsDelivery(t *testing.T) {
	sender := &recordingSender{}
	mux, st := realStoreHandler(t, sender)

	endpoint := registerVia(t, mux, "device-abc")

	body, _ := json.Marshal(unregisterRequest{Endpoint: endpoint})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/relay/unregister", bytes.NewReader(body)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unregister returned %d", rec.Code)
	}

	if rec := pushToURL(mux, endpoint, []byte("ciphertext")); rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after unregister, got %d", rec.Code)
	}
	if got := st.CountWebPushEndpoints(); got != 0 {
		t.Errorf("expected no endpoints left, got %d", got)
	}
}

// Endpoint ids are base64url and may contain "-" and "_". Those must survive
// the route pattern and the store lookup unchanged.
func TestIntegration_EndpointIDsWithURLSafeCharacters(t *testing.T) {
	sender := &recordingSender{}
	mux, _ := realStoreHandler(t, sender)

	// Register repeatedly until an id containing both characters turns up,
	// so the assertion covers them rather than hoping for a lucky draw.
	var endpoint string
	for i := 0; i < 200; i++ {
		e := registerVia(t, mux, fmt.Sprintf("device-%d", i))
		id := e[len("https://push.example.org/relay/"):]
		if bytes.ContainsRune([]byte(id), '-') && bytes.ContainsRune([]byte(id), '_') {
			endpoint = e
			break
		}
	}
	if endpoint == "" {
		t.Skip("no id with both - and _ generated in 200 tries")
	}

	if rec := pushToURL(mux, endpoint, []byte("ciphertext")); rec.Code != http.StatusCreated {
		t.Errorf("endpoint with URL-safe characters returned %d, want 201", rec.Code)
	}
}

// Guards the store's contract that an unknown id is a typed error rather than
// a zero value, which the handler relies on to pick 404 over 500.
func TestIntegration_UnknownEndpointIsTypedError(t *testing.T) {
	_, st := realStoreHandler(t, &recordingSender{})

	if _, err := st.GetWebPushEndpoint("never-issued"); !errors.Is(err, store.ErrEndpointNotFound) {
		t.Errorf("expected ErrEndpointNotFound, got %v", err)
	}
}
