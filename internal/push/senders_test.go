package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func testNotification(token, platform string) Notification {
	return Notification{
		Token:    token,
		Platform: platform,
		Title:    "New like",
		Body:     "Alice liked your post",
		Data: map[string]string{
			"reason":   "like",
			"uri":      "at://did:plc:alice/app.bsky.feed.like/3kco",
			"actorDid": "did:plc:alice",
		},
	}
}

// --- Expo ---

func TestExpoSend(t *testing.T) {
	var gotAuth string
	var gotMsg expoMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotMsg)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	e := NewExpoPushSender("expo-access-token")
	e.baseURL = srv.URL

	if err := e.Send(testNotification("ExponentPushToken[abc]", "ios")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer expo-access-token" {
		t.Errorf("expected access token in Authorization header, got %q", gotAuth)
	}
	if gotMsg.To != "ExponentPushToken[abc]" {
		t.Errorf("expected token in 'to', got %q", gotMsg.To)
	}
	if gotMsg.Title != "New like" || gotMsg.Body != "Alice liked your post" {
		t.Errorf("unexpected title/body: %q / %q", gotMsg.Title, gotMsg.Body)
	}
	if !gotMsg.MutableContent {
		t.Error("expected mutableContent to be set")
	}
	if gotMsg.Data["reason"] != "like" {
		t.Errorf("expected data.reason like, got %q", gotMsg.Data["reason"])
	}
}

func TestExpoSend_NoAuthHeaderWithoutToken(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") != ""
		w.WriteHeader(200)
	}))
	defer srv.Close()

	e := NewExpoPushSender("")
	e.baseURL = srv.URL
	if err := e.Send(testNotification("ExponentPushToken[abc]", "ios")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawAuth {
		t.Error("expected no Authorization header without access token")
	}
}

func TestExpoSend_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	e := NewExpoPushSender("")
	e.baseURL = srv.URL
	if err := e.Send(testNotification("ExponentPushToken[abc]", "ios")); err == nil {
		t.Error("expected error for HTTP 500")
	}
}

// --- APNs ---

func newTestAPNsKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newTestAPNsSender(t *testing.T, baseURL string) *APNsSender {
	t.Helper()
	a, err := NewAPNsSenderFromBytes(newTestAPNsKeyPEM(t), "KEYID12345", "TEAMID1234", "org.example.app", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a.baseURL = baseURL
	return a
}

func TestNewAPNsSenderFromBytes_RejectsBadKey(t *testing.T) {
	if _, err := NewAPNsSenderFromBytes([]byte("not pem"), "k", "t", "topic", false); err == nil {
		t.Error("expected error for non-PEM key")
	}
	if _, err := NewAPNsSenderFromBytes(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("junk")}), "k", "t", "topic", false); err == nil {
		t.Error("expected error for invalid PKCS8 data")
	}
}

func TestNewAPNsSender_SandboxBaseURL(t *testing.T) {
	a, err := NewAPNsSenderFromBytes(newTestAPNsKeyPEM(t), "k", "t", "topic", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.baseURL != "https://api.sandbox.push.apple.com" {
		t.Errorf("expected sandbox base URL, got %q", a.baseURL)
	}
	a, err = NewAPNsSenderFromBytes(newTestAPNsKeyPEM(t), "k", "t", "topic", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.baseURL != "https://api.push.apple.com" {
		t.Errorf("expected production base URL, got %q", a.baseURL)
	}
}

func TestAPNsSend(t *testing.T) {
	var gotPath, gotTopic, gotPushType string
	var gotPayload apnsPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTopic = r.Header.Get("apns-topic")
		gotPushType = r.Header.Get("apns-push-type")
		if r.Header.Get("Authorization") == "" {
			t.Error("expected Authorization header")
		}
		json.NewDecoder(r.Body).Decode(&gotPayload)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	a := newTestAPNsSender(t, srv.URL)
	if err := a.Send(testNotification("d1f2e3", "ios")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/3/device/d1f2e3" {
		t.Errorf("expected device token in path, got %q", gotPath)
	}
	if gotTopic != "org.example.app" {
		t.Errorf("expected apns-topic org.example.app, got %q", gotTopic)
	}
	if gotPushType != "alert" {
		t.Errorf("expected apns-push-type alert, got %q", gotPushType)
	}
	if gotPayload.APS.Alert.Title != "New like" {
		t.Errorf("unexpected alert title %q", gotPayload.APS.Alert.Title)
	}
	if gotPayload.APS.MutableContent != 1 {
		t.Error("expected mutable-content 1")
	}
	if gotPayload.Data["reason"] != "like" {
		t.Errorf("expected data.reason like, got %q", gotPayload.Data["reason"])
	}
}

func TestAPNsSend_UnregisteredTokenIsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
		json.NewEncoder(w).Encode(map[string]string{"reason": "Unregistered"})
	}))
	defer srv.Close()

	a := newTestAPNsSender(t, srv.URL)
	err := a.Send(testNotification("d1f2e3", "ios"))
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for APNs 410 Unregistered, got %v", err)
	}
}

func TestAPNsSend_BadDeviceTokenIsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"reason": "BadDeviceToken"})
	}))
	defer srv.Close()

	a := newTestAPNsSender(t, srv.URL)
	err := a.Send(testNotification("d1f2e3", "ios"))
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for BadDeviceToken, got %v", err)
	}
}

func TestAPNsSend_OtherErrorIsNotInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		json.NewEncoder(w).Encode(map[string]string{"reason": "TooManyRequests"})
	}))
	defer srv.Close()

	a := newTestAPNsSender(t, srv.URL)
	err := a.Send(testNotification("d1f2e3", "ios"))
	if err == nil {
		t.Fatal("expected error for HTTP 429")
	}
	if errors.Is(err, ErrTokenInvalid) {
		t.Error("transient APNs errors must not mark the token invalid")
	}
}

func TestAPNsGetToken_CachesUntilExpiry(t *testing.T) {
	a := newTestAPNsSender(t, "http://unused")

	tok1, err := a.getToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok2, err := a.getToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok1 != tok2 {
		t.Error("expected cached token on second call")
	}

	// Force expiry → token must be refreshed
	a.mu.Lock()
	a.tokenExp = time.Now().Add(-time.Minute)
	a.mu.Unlock()
	tok3, err := a.getToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok3 == tok1 {
		t.Error("expected fresh token after expiry")
	}
}

// --- FCM ---

func newTestFCMSender(baseURL string) *FCMSender {
	return &FCMSender{
		projectID:   "test-project",
		tokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "fcm-oauth-token"}),
		client:      &http.Client{Timeout: 5 * time.Second},
		baseURL:     baseURL,
	}
}

func TestNewFCMSenderFromBytes_RejectsBadInput(t *testing.T) {
	if _, err := NewFCMSenderFromBytes([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
	if _, err := NewFCMSenderFromBytes([]byte(`{"type":"service_account"}`)); err == nil {
		t.Error("expected error for missing project_id")
	}
}

func TestFCMSend(t *testing.T) {
	var gotPath, gotAuth string
	var gotReq fcmRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotReq)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	f := newTestFCMSender(srv.URL)
	if err := f.Send(testNotification("fcm-device-token", "android")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/v1/projects/test-project/messages:send" {
		t.Errorf("unexpected path %q", gotPath)
	}
	if gotAuth != "Bearer fcm-oauth-token" {
		t.Errorf("expected OAuth token in Authorization, got %q", gotAuth)
	}
	if gotReq.Message.Token != "fcm-device-token" {
		t.Errorf("expected device token in message, got %q", gotReq.Message.Token)
	}
	if gotReq.Message.Android == nil || gotReq.Message.Android.Notification.ChannelID != "like" {
		t.Error("expected android channel_id to equal the reason")
	}
}

func TestFCMSend_UnregisteredTokenIsInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"status": "UNREGISTERED", "message": "gone"},
		})
	}))
	defer srv.Close()

	f := newTestFCMSender(srv.URL)
	err := f.Send(testNotification("fcm-device-token", "android"))
	if !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for FCM UNREGISTERED, got %v", err)
	}
}

func TestFCMSend_OtherErrorIsNotInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]string{"status": "INTERNAL", "message": "boom"},
		})
	}))
	defer srv.Close()

	f := newTestFCMSender(srv.URL)
	err := f.Send(testNotification("fcm-device-token", "android"))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if errors.Is(err, ErrTokenInvalid) {
		t.Error("transient FCM errors must not mark the token invalid")
	}
}

// --- MultiSender routing ---

func TestMultiSenderRouting(t *testing.T) {
	var expoCalls, apnsCalls, fcmCalls atomic.Int64
	expoSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expoCalls.Add(1)
		w.WriteHeader(200)
	}))
	defer expoSrv.Close()
	apnsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apnsCalls.Add(1)
		w.WriteHeader(200)
	}))
	defer apnsSrv.Close()
	fcmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fcmCalls.Add(1)
		w.WriteHeader(200)
	}))
	defer fcmSrv.Close()

	m := NewMultiSender("")
	m.Expo.baseURL = expoSrv.URL

	// Without APNs/FCM configured, native tokens are skipped without error
	if err := m.Send(testNotification("native-ios-token", "ios")); err != nil {
		t.Errorf("expected nil error for native iOS token without APNs, got %v", err)
	}
	if err := m.Send(testNotification("native-android-token", "android")); err != nil {
		t.Errorf("expected nil error for native Android token without FCM, got %v", err)
	}
	if expoCalls.Load() != 0 {
		t.Error("expected no Expo calls for native tokens")
	}

	m.APNs = newTestAPNsSender(t, apnsSrv.URL)
	m.FCM = newTestFCMSender(fcmSrv.URL)

	tests := []struct {
		token, platform string
		wantExpo        int64
		wantAPNs        int64
		wantFCM         int64
	}{
		{"ExponentPushToken[a]", "ios", 1, 0, 0},
		{"ExponentPushToken[b]", "android", 2, 0, 0},
		{"native-ios-token", "ios", 2, 1, 0},
		{"native-android-token", "android", 2, 1, 1},
	}
	for _, tt := range tests {
		if err := m.Send(testNotification(tt.token, tt.platform)); err != nil {
			t.Errorf("Send(%s/%s): unexpected error: %v", tt.platform, tt.token, err)
		}
		if expoCalls.Load() != tt.wantExpo || apnsCalls.Load() != tt.wantAPNs || fcmCalls.Load() != tt.wantFCM {
			t.Errorf("after %s/%s: calls expo=%d apns=%d fcm=%d, want %d/%d/%d",
				tt.platform, tt.token, expoCalls.Load(), apnsCalls.Load(), fcmCalls.Load(),
				tt.wantExpo, tt.wantAPNs, tt.wantFCM)
		}
	}

	if err := m.Send(testNotification("t", "web")); err == nil {
		t.Error("expected error for web platform")
	}
	if err := m.Send(testNotification("t", "windows")); err == nil {
		t.Error("expected error for unsupported platform")
	}
}
