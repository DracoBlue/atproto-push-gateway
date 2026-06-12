package jetstream

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

// fakeSender records notifications and can simulate send failures.
type fakeSender struct {
	mu    sync.Mutex
	sent  []push.Notification
	errFn func(n push.Notification) error
}

func (f *fakeSender) Send(n push.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errFn != nil {
		if err := f.errFn(n); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, n)
	return nil
}

func (f *fakeSender) notifications() []push.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]push.Notification, len(f.sent))
	copy(out, f.sent)
	return out
}

func newTestConsumer(t *testing.T) (*Consumer, *store.Store, *fakeSender) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	sender := &fakeSender{}
	c := NewConsumer("wss://unused.example.org/subscribe", s, sender, nil)
	return c, s, sender
}

func commitEvent(operation, collection, rkey string, record interface{}) *CommitEvent {
	var raw json.RawMessage
	if record != nil {
		raw, _ = json.Marshal(record)
	}
	return &CommitEvent{
		Operation:  operation,
		Collection: collection,
		RKey:       rkey,
		Record:     raw,
	}
}

func TestHandleCommit_LikeNotifiesRegisteredTarget(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.like", "3kco", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc", "cid": "x"},
	}))

	sent := sender.notifications()
	if len(sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(sent))
	}
	n := sent[0]
	if n.Token != "ExponentPushToken[bob]" {
		t.Errorf("unexpected token %q", n.Token)
	}
	if n.Data["reason"] != "like" {
		t.Errorf("expected reason like, got %q", n.Data["reason"])
	}
	if n.Data["uri"] != "at://did:plc:alice/app.bsky.feed.like/3kco" {
		t.Errorf("unexpected uri %q", n.Data["uri"])
	}
	if n.Data["subject"] != "at://did:plc:bob/app.bsky.feed.post/abc" {
		t.Errorf("unexpected subject %q", n.Data["subject"])
	}
	if n.Data["recipientDid"] != "did:plc:bob" || n.Data["actorDid"] != "did:plc:alice" {
		t.Errorf("unexpected recipient/actor: %q / %q", n.Data["recipientDid"], n.Data["actorDid"])
	}
	if n.Title != "New like" || n.Body != "Someone liked your post" {
		t.Errorf("unexpected title/body: %q / %q", n.Title, n.Body)
	}
	if c.GetStats().MatchedEvents != 1 {
		t.Errorf("expected 1 matched event, got %d", c.GetStats().MatchedEvents)
	}
}

func TestHandleCommit_LikeIgnoresUnregisteredAndSelf(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	// Like on an unregistered DID's post
	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.like", "1", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:carol/app.bsky.feed.post/abc"},
	}))
	// Self-like
	c.handleCommit("did:plc:bob", commitEvent("create", "app.bsky.feed.like", "2", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
	}))
	// Delete operation must not notify
	c.handleCommit("did:plc:alice", commitEvent("delete", "app.bsky.feed.like", "3", nil))
	// Malformed record must not notify
	c.handleCommit("did:plc:alice", &CommitEvent{Operation: "create", Collection: "app.bsky.feed.like", RKey: "4", Record: json.RawMessage(`{`)})

	if sent := sender.notifications(); len(sent) != 0 {
		t.Errorf("expected no notifications, got %d", len(sent))
	}
}

func TestHandleCommit_LikeViaRepostNotifiesReposter(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")
	s.RegisterToken("did:plc:carol", "ios", "ExponentPushToken[carol]", "org.example.app")

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.like", "3kco", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
		"via":     map[string]string{"uri": "at://did:plc:carol/app.bsky.feed.repost/xyz"},
	}))

	sent := sender.notifications()
	if len(sent) != 2 {
		t.Fatalf("expected 2 notifications (author + reposter), got %d", len(sent))
	}
	reasons := map[string]string{}
	for _, n := range sent {
		reasons[n.Data["recipientDid"]] = n.Data["reason"]
	}
	if reasons["did:plc:bob"] != "like" {
		t.Errorf("expected like for author, got %q", reasons["did:plc:bob"])
	}
	if reasons["did:plc:carol"] != "like-via-repost" {
		t.Errorf("expected like-via-repost for reposter, got %q", reasons["did:plc:carol"])
	}
}

func TestHandleCommit_RepostNotifies(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.repost", "3kco", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
	}))

	sent := sender.notifications()
	if len(sent) != 1 || sent[0].Data["reason"] != "repost" {
		t.Fatalf("expected 1 repost notification, got %+v", sent)
	}
}

func TestHandleCommit_PostReplyQuoteMention(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")
	s.RegisterToken("did:plc:carol", "ios", "ExponentPushToken[carol]", "org.example.app")
	s.RegisterToken("did:plc:dave", "ios", "ExponentPushToken[dave]", "org.example.app")

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.post", "3kco", map[string]interface{}{
		"text": "hey @dave look at this",
		"reply": map[string]interface{}{
			"parent": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/parent"},
			"root":   map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/root"},
		},
		"embed": map[string]interface{}{
			"$type":  "app.bsky.embed.record",
			"record": map[string]string{"uri": "at://did:plc:carol/app.bsky.feed.post/quoted"},
		},
		"facets": []map[string]interface{}{
			{"features": []map[string]string{{"$type": "app.bsky.richtext.facet#mention", "did": "did:plc:dave"}}},
		},
	}))

	sent := sender.notifications()
	if len(sent) != 3 {
		t.Fatalf("expected 3 notifications (reply, quote, mention), got %d", len(sent))
	}
	reasons := map[string]string{}
	for _, n := range sent {
		reasons[n.Data["recipientDid"]] = n.Data["reason"]
		if n.Data["uri"] != "at://did:plc:alice/app.bsky.feed.post/3kco" {
			t.Errorf("unexpected uri %q", n.Data["uri"])
		}
	}
	if reasons["did:plc:bob"] != "reply" || reasons["did:plc:carol"] != "quote" || reasons["did:plc:dave"] != "mention" {
		t.Errorf("unexpected reasons: %+v", reasons)
	}
}

func TestHandleCommit_FollowNotifies(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.graph.follow", "3kco", map[string]string{
		"subject": "did:plc:bob",
	}))

	sent := sender.notifications()
	if len(sent) != 1 || sent[0].Data["reason"] != "follow" {
		t.Fatalf("expected 1 follow notification, got %+v", sent)
	}
	if sent[0].Data["uri"] != "at://did:plc:alice/app.bsky.graph.follow/3kco" {
		t.Errorf("unexpected uri %q", sent[0].Data["uri"])
	}
}

func TestHandleCommit_BlockSuppressesNotifications(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	like := func(rkey string) *CommitEvent {
		return commitEvent("create", "app.bsky.feed.like", rkey, map[string]interface{}{
			"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
		})
	}

	// Recipient blocked the actor → suppressed
	s.AddBlock("did:plc:bob", "did:plc:alice", "r1")
	c.handleCommit("did:plc:alice", like("1"))
	if sent := sender.notifications(); len(sent) != 0 {
		t.Fatalf("expected suppression when recipient blocked actor, got %d", len(sent))
	}
	s.RemoveBlock("did:plc:bob", "did:plc:alice")

	// Actor blocked the recipient → suppressed too
	s.AddBlock("did:plc:alice", "did:plc:bob", "r2")
	c.handleCommit("did:plc:alice", like("2"))
	if sent := sender.notifications(); len(sent) != 0 {
		t.Fatalf("expected suppression when actor blocked recipient, got %d", len(sent))
	}
	s.RemoveBlock("did:plc:alice", "did:plc:bob")

	// No block → delivered
	c.handleCommit("did:plc:alice", like("3"))
	if sent := sender.notifications(); len(sent) != 1 {
		t.Fatalf("expected delivery without block, got %d", len(sent))
	}
}

func TestHandleCommit_BlockCreateAndDelete(t *testing.T) {
	c, s, _ := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	// Block where the subject is registered → tracked
	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.graph.block", "rkey1", map[string]string{
		"subject": "did:plc:bob",
	}))
	if !s.IsBlocked("did:plc:bob", "did:plc:alice") {
		t.Fatal("expected block to be tracked")
	}

	// Unblock via rkey
	c.handleCommit("did:plc:alice", commitEvent("delete", "app.bsky.graph.block", "rkey1", nil))
	if s.IsBlocked("did:plc:bob", "did:plc:alice") {
		t.Fatal("expected block to be removed after delete")
	}

	// Block between two unregistered DIDs → not tracked
	c.handleCommit("did:plc:carol", commitEvent("create", "app.bsky.graph.block", "rkey2", map[string]string{
		"subject": "did:plc:dave",
	}))
	if s.IsBlocked("did:plc:dave", "did:plc:carol") {
		t.Error("expected block between unregistered DIDs to be ignored")
	}
}

func TestHandleCommit_VerificationCreateAndDelete(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	c.handleCommit("did:plc:verifier", commitEvent("create", "app.bsky.graph.verification", "rkey1", map[string]string{
		"subject": "did:plc:bob",
		"handle":  "bob.test",
	}))
	sent := sender.notifications()
	if len(sent) != 1 || sent[0].Data["reason"] != "verified" {
		t.Fatalf("expected verified notification, got %+v", sent)
	}
	if sent[0].Body != "Your account has been verified" {
		t.Errorf("unexpected body %q", sent[0].Body)
	}

	c.handleCommit("did:plc:verifier", commitEvent("delete", "app.bsky.graph.verification", "rkey1", nil))
	sent = sender.notifications()
	if len(sent) != 2 || sent[1].Data["reason"] != "unverified" {
		t.Fatalf("expected unverified notification, got %+v", sent)
	}

	// Deleting again is a no-op
	c.handleCommit("did:plc:verifier", commitEvent("delete", "app.bsky.graph.verification", "rkey1", nil))
	if len(sender.notifications()) != 2 {
		t.Error("expected no notification for unknown verification rkey")
	}
}

func TestSendNotification_RemovesInvalidToken(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[stale]", "org.example.app")
	sender.errFn = func(n push.Notification) error {
		return fmt.Errorf("%w: APNs 410 Unregistered", push.ErrTokenInvalid)
	}

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.like", "3kco", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
	}))

	tokens, err := s.GetTokensForDID("did:plc:bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected invalid token to be removed, still have %d", len(tokens))
	}
	if c.GetStats().PushErrors != 1 {
		t.Errorf("expected 1 push error, got %d", c.GetStats().PushErrors)
	}
}

func TestSendNotification_TransientErrorKeepsToken(t *testing.T) {
	c, s, sender := newTestConsumer(t)
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")
	sender.errFn = func(n push.Notification) error {
		return fmt.Errorf("expo push API returned 500")
	}

	c.handleCommit("did:plc:alice", commitEvent("create", "app.bsky.feed.like", "3kco", map[string]interface{}{
		"subject": map[string]string{"uri": "at://did:plc:bob/app.bsky.feed.post/abc"},
	}))

	tokens, _ := s.GetTokensForDID("did:plc:bob")
	if len(tokens) != 1 {
		t.Errorf("expected token to survive transient error, have %d", len(tokens))
	}
}

func TestNotifyTokenRegistered_Idempotent(t *testing.T) {
	c, _, _ := newTestConsumer(t)

	select {
	case <-c.startCh:
		t.Fatal("expected startCh open with empty store")
	default:
	}

	c.NotifyTokenRegistered()
	c.NotifyTokenRegistered() // must not panic on double close

	select {
	case <-c.startCh:
	default:
		t.Fatal("expected startCh closed after NotifyTokenRegistered")
	}
}

func TestNewConsumer_StartsImmediatelyWithExistingTokens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.RegisterToken("did:plc:bob", "ios", "ExponentPushToken[bob]", "org.example.app")

	c := NewConsumer("wss://unused.example.org/subscribe", s, &fakeSender{}, nil)
	select {
	case <-c.startCh:
	default:
		t.Error("expected startCh closed when tokens already exist")
	}
}

func TestRun_StopsBeforeStart(t *testing.T) {
	c, _, _ := newTestConsumer(t)

	done := make(chan struct{})
	go func() {
		c.Run()
		close(done)
	}()
	c.Stop()
	<-done // Run must return promptly once stopped
}
