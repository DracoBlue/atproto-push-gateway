package jetstream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
)

// newWSServer starts a test WebSocket server that runs handler on each connection.
func newWSServer(t *testing.T, handler func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		handler(conn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func testEventJSON(t *testing.T, timeUS int64) []byte {
	t.Helper()
	msg, err := json.Marshal(map[string]interface{}{
		"did":     "did:plc:alice",
		"time_us": timeUS,
		"kind":    "commit",
		"commit": map[string]interface{}{
			"operation":  "create",
			"collection": "app.bsky.feed.like",
			"rkey":       "3kco",
			"record": map[string]interface{}{
				"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestConnect_ReceivesTextEvent(t *testing.T) {
	c, _, _ := newTestConsumer(t)

	srv := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, testEventJSON(t, 1700000001))
		// non-commit event is counted but not dispatched
		conn.WriteMessage(websocket.TextMessage, []byte(`{"did":"did:plc:x","time_us":1700000002,"kind":"identity"}`))
		// invalid JSON is skipped
		conn.WriteMessage(websocket.TextMessage, []byte(`{`))
	})

	// connect returns with a read error once the server closes the connection
	if err := c.connect(wsURL(srv)); err == nil {
		t.Error("expected read error after server close")
	}

	select {
	case item := <-c.commitCh:
		if item.actorDID != "did:plc:alice" {
			t.Errorf("unexpected actor DID %q", item.actorDID)
		}
		if item.commit.Collection != "app.bsky.feed.like" || item.commit.RKey != "3kco" {
			t.Errorf("unexpected commit: %+v", item.commit)
		}
	default:
		t.Fatal("expected a dispatched commit item")
	}
	select {
	case item := <-c.commitCh:
		t.Fatalf("expected exactly one commit item, got extra: %+v", item)
	default:
	}

	stats := c.GetStats()
	if stats.EventsReceived != 3 {
		t.Errorf("expected 3 received events, got %d", stats.EventsReceived)
	}
	if stats.LastCursor != 1700000002 {
		t.Errorf("expected cursor 1700000002, got %d", stats.LastCursor)
	}
}

func TestConnect_DecompressesZstdBinaryFrames(t *testing.T) {
	c, _, _ := newTestConsumer(t)

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderDict(zstdDictionary))
	if err != nil {
		t.Fatal(err)
	}
	compressed := enc.EncodeAll(testEventJSON(t, 1700000003), nil)
	enc.Close()

	srv := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.BinaryMessage, compressed)
		// junk binary frame is logged and skipped, connection continues
		conn.WriteMessage(websocket.BinaryMessage, []byte("not zstd"))
	})

	c.connect(wsURL(srv))

	select {
	case item := <-c.commitCh:
		if item.actorDID != "did:plc:alice" {
			t.Errorf("unexpected actor DID %q", item.actorDID)
		}
	default:
		t.Fatal("expected a dispatched commit item from the compressed frame")
	}
	if cursor := c.GetStats().LastCursor; cursor != 1700000003 {
		t.Errorf("expected cursor 1700000003, got %d", cursor)
	}
}

func TestConnect_DropsWhenQueueFull(t *testing.T) {
	c, _, _ := newTestConsumer(t)
	// Shrink the queue so the second event overflows (no workers draining it)
	c.commitCh = make(chan dispatchItem, 1)

	srv := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, testEventJSON(t, 1))
		conn.WriteMessage(websocket.TextMessage, testEventJSON(t, 2))
	})

	c.connect(wsURL(srv))

	if dropped := c.GetStats().EventsDropped; dropped != 1 {
		t.Errorf("expected 1 dropped event, got %d", dropped)
	}
}

func TestConnect_DialError(t *testing.T) {
	c, _, _ := newTestConsumer(t)
	if err := c.connect("ws://127.0.0.1:1/subscribe"); err == nil {
		t.Error("expected dial error")
	}
}

func TestRun_ConnectsAfterTokenRegistration(t *testing.T) {
	c, s, sender := newTestConsumer(t)

	srv := newWSServer(t, func(conn *websocket.Conn) {
		conn.WriteMessage(websocket.TextMessage, testEventJSON(t, 1700000004))
		// Keep the connection open briefly so the consumer processes the event
		time.Sleep(100 * time.Millisecond)
	})
	c.url = wsURL(srv)

	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	done := make(chan struct{})
	go func() {
		c.Run()
		close(done)
	}()
	c.NotifyTokenRegistered()

	// Wait for the event to flow through connect → dispatch → sendNotification
	deadline := time.After(5 * time.Second)
	for {
		if len(sender.notifications()) >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for notification")
		case <-time.After(10 * time.Millisecond):
		}
	}

	c.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}

	sent := sender.notifications()
	if sent[0].Data["reason"] != "like" || sent[0].Data["recipientDid"] != "did:plc:bob" {
		t.Errorf("unexpected notification: %+v", sent[0])
	}
}
