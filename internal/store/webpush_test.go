package store

import (
	"errors"
	"testing"
)

// newTestStore lives in store_test.go.

func TestWebPushEndpoint_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	want := WebPushEndpoint{
		Platform:       "ios",
		DeviceToken:    "device-abc",
		AppID:          "app.kiesel.Kiesel",
		VAPIDPublicKey: "BCk-public-key",
		Instance:       "mastodon.social",
	}

	id, err := s.CreateWebPushEndpoint(want)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	got, err := s.GetWebPushEndpoint(id)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestWebPushEndpoint_IDsAreUnguessable(t *testing.T) {
	s := newTestStore(t)

	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		id, err := s.CreateWebPushEndpoint(WebPushEndpoint{
			Platform: "ios", DeviceToken: "device-" + string(rune('a'+i)), AppID: "a",
		})
		if err != nil {
			t.Fatalf("create failed: %v", err)
		}
		if seen[id] {
			t.Fatalf("duplicate endpoint id: %q", id)
		}
		// 32 random bytes in base64url.
		if len(id) < 40 {
			t.Errorf("endpoint id too short to be unguessable: %q (%d chars)", id, len(id))
		}
		seen[id] = true
	}
}

// The raw id must not be recoverable from the database: only its hash is
// stored, so a leaked database does not yield working endpoints.
func TestWebPushEndpoint_StoresOnlyHash(t *testing.T) {
	s := newTestStore(t)

	id, err := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "ios", DeviceToken: "device-abc", AppID: "a",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	var stored string
	if err := s.db.QueryRow("SELECT endpoint_hash FROM webpush_endpoints").Scan(&stored); err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if stored == id {
		t.Error("the raw endpoint id was stored; expected only its hash")
	}
	if stored != hashEndpointID(id) {
		t.Error("stored value is not the hash of the id")
	}
}

func TestWebPushEndpoint_UnknownIDNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetWebPushEndpoint("never-registered")
	if !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("expected ErrEndpointNotFound, got %v", err)
	}
}

// Re-registering the same device must replace its endpoint, not accumulate
// rows that would keep pushing to a subscription the client has replaced.
func TestWebPushEndpoint_ReregisterReplacesPrevious(t *testing.T) {
	s := newTestStore(t)

	e := WebPushEndpoint{Platform: "ios", DeviceToken: "device-abc", AppID: "app.kiesel.Kiesel"}
	first, err := s.CreateWebPushEndpoint(e)
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}
	second, err := s.CreateWebPushEndpoint(e)
	if err != nil {
		t.Fatalf("second create failed: %v", err)
	}

	if _, err := s.GetWebPushEndpoint(first); !errors.Is(err, ErrEndpointNotFound) {
		t.Error("the previous endpoint should have been removed")
	}
	if _, err := s.GetWebPushEndpoint(second); err != nil {
		t.Errorf("the new endpoint should resolve: %v", err)
	}
	if got := s.CountWebPushEndpoints(); got != 1 {
		t.Errorf("expected exactly 1 endpoint, got %d", got)
	}
}

// A different app on the same device is a separate subscription.
func TestWebPushEndpoint_DifferentAppIDCoexists(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "ios", DeviceToken: "device-abc", AppID: "app.one",
	})
	b, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "ios", DeviceToken: "device-abc", AppID: "app.two",
	})

	if _, err := s.GetWebPushEndpoint(a); err != nil {
		t.Errorf("first app's endpoint should survive: %v", err)
	}
	if _, err := s.GetWebPushEndpoint(b); err != nil {
		t.Errorf("second app's endpoint should resolve: %v", err)
	}
}

func TestWebPushEndpoint_Delete(t *testing.T) {
	s := newTestStore(t)

	id, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "android", DeviceToken: "fcm-token", AppID: "a",
	})

	if err := s.DeleteWebPushEndpoint(id); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := s.GetWebPushEndpoint(id); !errors.Is(err, ErrEndpointNotFound) {
		t.Error("endpoint should be gone")
	}
	// Deleting again is not an error: the caller's goal is "this must be gone".
	if err := s.DeleteWebPushEndpoint(id); err != nil {
		t.Errorf("repeated delete should be a no-op, got %v", err)
	}
}

func TestWebPushEndpoint_DeleteForDevice(t *testing.T) {
	s := newTestStore(t)

	id, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "android", DeviceToken: "fcm-token", AppID: "app.kiesel.Kiesel",
	})
	other, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "android", DeviceToken: "other-token", AppID: "app.kiesel.Kiesel",
	})

	if err := s.DeleteWebPushEndpointsForDevice("fcm-token", "app.kiesel.Kiesel"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := s.GetWebPushEndpoint(id); !errors.Is(err, ErrEndpointNotFound) {
		t.Error("the device's endpoint should be gone")
	}
	if _, err := s.GetWebPushEndpoint(other); err != nil {
		t.Error("another device's endpoint must be untouched")
	}
}

func TestWebPushEndpoint_RejectsUnknownPlatform(t *testing.T) {
	s := newTestStore(t)

	_, err := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "web", DeviceToken: "t", AppID: "a",
	})
	if err == nil {
		t.Error("expected the platform CHECK constraint to reject \"web\"")
	}
}

// The relay tables must stay out of the ATproto in-memory indexes, which are
// keyed by DID and sized for the firehose matching path.
func TestWebPushEndpoint_DoesNotAffectDIDIndex(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "ios", DeviceToken: "device-abc", AppID: "a",
	}); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if s.HasRegisteredDIDs() {
		t.Error("a relay endpoint must not register a DID")
	}
	tokens, _, dids := s.GetStats()
	if tokens != 0 || dids != 0 {
		t.Errorf("relay endpoints leaked into ATproto stats: tokens=%d dids=%d", tokens, dids)
	}
}

func TestWebPushEndpoint_TouchRecordsPush(t *testing.T) {
	s := newTestStore(t)

	id, _ := s.CreateWebPushEndpoint(WebPushEndpoint{
		Platform: "ios", DeviceToken: "device-abc", AppID: "a",
	})

	var before *string
	s.db.QueryRow("SELECT last_push_at FROM webpush_endpoints").Scan(&before)
	if before != nil {
		t.Error("last_push_at should start empty")
	}

	if err := s.TouchWebPushEndpoint(id); err != nil {
		t.Fatalf("touch failed: %v", err)
	}

	var after *string
	s.db.QueryRow("SELECT last_push_at FROM webpush_endpoints").Scan(&after)
	if after == nil {
		t.Error("last_push_at should be set after a touch")
	}
}
