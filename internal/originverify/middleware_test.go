package originverify

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func do(t *testing.T, h http.Handler, method, path, headerName, headerVal string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if headerName != "" {
		req.Header.Set(headerName, headerVal)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWrap_NoSecret_PassesThrough(t *testing.T) {
	h := Wrap(okHandler(), Config{})
	rec := do(t, h, "GET", "/anything", "", "")
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestWrap_MissingHeader_Forbidden(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret"})
	rec := do(t, h, "GET", "/health", "", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestWrap_WrongSecret_Forbidden(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret"})
	rec := do(t, h, "GET", "/health", "X-Origin-Verify", "wrong")
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

func TestWrap_CorrectSecret_Allowed(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret"})
	rec := do(t, h, "GET", "/health", "X-Origin-Verify", "topsecret")
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestWrap_CustomHeaderName(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret", HeaderName: "X-CDN-Auth"})
	if rec := do(t, h, "GET", "/health", "X-Origin-Verify", "topsecret"); rec.Code != http.StatusForbidden {
		t.Errorf("default header should be ignored when custom set, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/health", "X-CDN-Auth", "topsecret"); rec.Code != http.StatusOK {
		t.Errorf("custom header should be accepted, got %d", rec.Code)
	}
}

func TestWrap_ExcludeHealth(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "s", ExcludeHealth: true})
	if rec := do(t, h, "GET", "/health", "", ""); rec.Code != http.StatusOK {
		t.Errorf("/health should be exempt, got %d", rec.Code)
	}
	// Non-exempt path still gated.
	if rec := do(t, h, "GET", "/other", "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("non-health path should still be gated, got %d", rec.Code)
	}
}

func TestWrap_ExcludeDIDJSON(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "s", ExcludeDIDJSON: true})
	if rec := do(t, h, "GET", "/.well-known/did.json", "", ""); rec.Code != http.StatusOK {
		t.Errorf("did.json should be exempt, got %d", rec.Code)
	}
	if rec := do(t, h, "GET", "/health", "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("/health should still be gated, got %d", rec.Code)
	}
}

func TestWrap_ExemptionDoesNotLeakSimilarPaths(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "s", ExcludeHealth: true})
	// /healthcheck should NOT be exempt (exact match only).
	if rec := do(t, h, "GET", "/healthcheck", "", ""); rec.Code != http.StatusForbidden {
		t.Errorf("/healthcheck should not be exempt, got %d", rec.Code)
	}
}

// The relay path serves arbitrary Fediverse servers, which cannot attach an
// origin-verify header. The exemption is unconditional: a 403 here would
// make Mastodon destroy the user's push subscription (it drops a
// subscription on any 4xx except 408/429), silently and permanently.
func TestWrap_RelayPathAlwaysExempt(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret"})

	for _, path := range []string{
		"/relay/register",
		"/relay/unregister",
		"/relay/some-opaque-endpoint-id",
	} {
		rec := do(t, h, "POST", path, "", "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: expected 200 without the origin header, got %d", path, rec.Code)
		}
	}
}

// The exemption must be anchored to the /relay/ prefix, not merely contained
// anywhere in the path, so it cannot be used to bypass the gate.
func TestWrap_RelayExemptionIsPrefixAnchored(t *testing.T) {
	h := Wrap(okHandler(), Config{Secret: "topsecret"})

	for _, path := range []string{
		"/xrpc/app.bsky.notification.registerPush?x=/relay/",
		"/sneaky/relay/thing",
		"/relay",
	} {
		rec := do(t, h, "POST", path, "", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403, got %d", path, rec.Code)
		}
	}
}
