package jetstream

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"

	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

type Event struct {
	DID        string          `json:"did"`
	TimeUS     int64           `json:"time_us"`
	Kind       string          `json:"kind"`
	Commit     *CommitEvent    `json:"commit,omitempty"`
}

type CommitEvent struct {
	Rev        string          `json:"rev"`
	Operation  string          `json:"operation"`
	Collection string          `json:"collection"`
	RKey       string          `json:"rkey"`
	Record     json.RawMessage `json:"record,omitempty"`
}

type LikeRecord struct {
	Subject struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	} `json:"subject"`
}

type RepostRecord struct {
	Subject struct {
		URI string `json:"uri"`
		CID string `json:"cid"`
	} `json:"subject"`
}

type PostRecord struct {
	Text  string `json:"text"`
	Reply *struct {
		Parent struct {
			URI string `json:"uri"`
		} `json:"parent"`
		Root struct {
			URI string `json:"uri"`
		} `json:"root"`
	} `json:"reply,omitempty"`
	Embed *struct {
		Type   string `json:"$type"`
		Record *struct {
			URI string `json:"uri"`
		} `json:"record,omitempty"`
	} `json:"embed,omitempty"`
	Facets []struct {
		Features []struct {
			Type string `json:"$type"`
			DID  string `json:"did,omitempty"`
		} `json:"features"`
	} `json:"facets,omitempty"`
}

type FollowRecord struct {
	Subject string `json:"subject"`
}

type BlockRecord struct {
	Subject string `json:"subject"`
}

type Consumer struct {
	url             string
	store           *store.Store
	sender          *push.MultiSender
	profileResolver *profile.Resolver
	lastCursor      atomic.Int64
	stopCh          chan struct{}
	startCh         chan struct{} // closed when first token registered

	// Stats
	eventsReceived atomic.Int64
	bytesReceived  atomic.Int64
	pushesSent     atomic.Int64
	pushErrors     atomic.Int64
	matchedEvents  atomic.Int64
}

type Stats struct {
	EventsReceived int64 `json:"eventsReceived"`
	BytesReceived  int64 `json:"bytesReceived"`
	PushesSent     int64 `json:"pushesSent"`
	PushErrors     int64 `json:"pushErrors"`
	MatchedEvents  int64 `json:"matchedEvents"`
	LastCursor     int64 `json:"lastCursor"`
}

func (c *Consumer) GetStats() Stats {
	return Stats{
		EventsReceived: c.eventsReceived.Load(),
		BytesReceived:  c.bytesReceived.Load(),
		PushesSent:     c.pushesSent.Load(),
		PushErrors:     c.pushErrors.Load(),
		MatchedEvents:  c.matchedEvents.Load(),
		LastCursor:     c.lastCursor.Load(),
	}
}

func NewConsumer(url string, s *store.Store, sender *push.MultiSender, profileResolver *profile.Resolver) *Consumer {
	c := &Consumer{
		url:             url,
		store:           s,
		sender:          sender,
		profileResolver: profileResolver,
		stopCh:          make(chan struct{}),
		startCh:         make(chan struct{}),
	}
	// If tokens already exist (from SQLite on restart), start immediately
	if s.HasRegisteredDIDs() {
		close(c.startCh)
	}
	return c
}

// Stop signals the consumer to stop reconnecting.
func (c *Consumer) Stop() {
	close(c.stopCh)
}

// NotifyTokenRegistered signals that a token was registered.
// If the consumer hasn't started yet, this will start it.
func (c *Consumer) NotifyTokenRegistered() {
	select {
	case <-c.startCh:
		// already started
	default:
		close(c.startCh)
	}
}

func (c *Consumer) Run() {
	// Wait until at least one token is registered before connecting to Jetstream
	select {
	case <-c.startCh:
		log.Println("[jetstream] first token registered, starting consumer")
	case <-c.stopCh:
		log.Println("[jetstream] consumer stopped before starting")
		return
	}

	collections := []string{
		"app.bsky.feed.like",
		"app.bsky.feed.repost",
		"app.bsky.feed.post",
		"app.bsky.graph.follow",
		"app.bsky.graph.block",
	}

	params := "?compress=true"
	for _, col := range collections {
		params += "&wantedCollections=" + col
	}

	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		select {
		case <-c.stopCh:
			log.Println("[jetstream] consumer stopped")
			return
		default:
		}

		connectURL := c.url + params
		// If we have a cursor from a previous connection, include it to resume
		if cursor := c.lastCursor.Load(); cursor > 0 {
			connectURL += fmt.Sprintf("&cursor=%d", cursor)
		}

		err := c.connect(connectURL)
		if err == nil {
			// Successful connection that ended normally; reset backoff
			backoff = time.Second
		}

		select {
		case <-c.stopCh:
			log.Println("[jetstream] consumer stopped")
			return
		default:
		}

		log.Printf("[jetstream] reconnecting in %v...", backoff)

		select {
		case <-time.After(backoff):
		case <-c.stopCh:
			log.Println("[jetstream] consumer stopped")
			return
		}

		// Exponential backoff: double each time, cap at maxBackoff
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Consumer) connect(url string) error {
	log.Printf("[jetstream] connecting to %s", url)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("[jetstream] dial error: %v", err)
		return err
	}
	defer conn.Close()

	// Create zstd decoder for compressed messages
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		log.Printf("[jetstream] failed to create zstd decoder: %v", err)
		return err
	}
	defer decoder.Close()

	log.Println("[jetstream] connected (zstd compression enabled)")

	for {
		select {
		case <-c.stopCh:
			return nil
		default:
		}

		msgType, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[jetstream] read error: %v", err)
			return err
		}

		c.bytesReceived.Add(int64(len(message)))

		// Decompress if binary (zstd compressed)
		if msgType == websocket.BinaryMessage {
			decompressed, err := decoder.DecodeAll(message, nil)
			if err != nil {
				log.Printf("[jetstream] zstd decompress error: %v", err)
				continue
			}
			message = decompressed
		}

		c.eventsReceived.Add(1)

		var event Event
		if err := json.Unmarshal(message, &event); err != nil {
			continue
		}

		// Track cursor for reconnect resume
		if event.TimeUS > 0 {
			c.lastCursor.Store(event.TimeUS)
		}

		if event.Kind != "commit" || event.Commit == nil {
			continue
		}

		c.handleCommit(event.DID, event.Commit)
	}
}

func (c *Consumer) handleCommit(actorDID string, commit *CommitEvent) {
	switch commit.Collection {
	case "app.bsky.feed.like":
		if commit.Operation == "create" {
			c.handleLike(actorDID, commit.Record)
		}
	case "app.bsky.feed.repost":
		if commit.Operation == "create" {
			c.handleRepost(actorDID, commit.Record)
		}
	case "app.bsky.feed.post":
		if commit.Operation == "create" {
			c.handlePost(actorDID, commit.Record)
		}
	case "app.bsky.graph.follow":
		if commit.Operation == "create" {
			c.handleFollow(actorDID, commit.Record)
		}
	case "app.bsky.graph.block":
		if commit.Operation == "create" {
			c.handleBlockCreate(actorDID, commit.RKey, commit.Record)
		} else if commit.Operation == "delete" {
			c.handleBlockDelete(actorDID, commit.RKey)
		}
	}
}

// extractDIDFromURI extracts the DID from an AT URI like at://did:plc:xxx/app.bsky.feed.post/yyy
func extractDIDFromURI(uri string) string {
	if !strings.HasPrefix(uri, "at://") {
		return ""
	}
	parts := strings.SplitN(uri[5:], "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (c *Consumer) handleLike(actorDID string, record json.RawMessage) {
	var like LikeRecord
	if err := json.Unmarshal(record, &like); err != nil {
		return
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	c.sendNotification(actorDID, targetDID, "like", like.Subject.URI)
}

func (c *Consumer) handleRepost(actorDID string, record json.RawMessage) {
	var repost RepostRecord
	if err := json.Unmarshal(record, &repost); err != nil {
		return
	}

	targetDID := extractDIDFromURI(repost.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	c.sendNotification(actorDID, targetDID, "repost", repost.Subject.URI)
}

func (c *Consumer) handlePost(actorDID string, record json.RawMessage) {
	var post PostRecord
	if err := json.Unmarshal(record, &post); err != nil {
		return
	}

	// Reply
	if post.Reply != nil {
		targetDID := extractDIDFromURI(post.Reply.Parent.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "reply", post.Reply.Parent.URI)
		}
	}

	// Quote
	if post.Embed != nil && post.Embed.Type == "app.bsky.embed.record" && post.Embed.Record != nil {
		targetDID := extractDIDFromURI(post.Embed.Record.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "quote", post.Embed.Record.URI)
		}
	}

	// Mentions
	for _, facet := range post.Facets {
		for _, feature := range facet.Features {
			if feature.Type == "app.bsky.richtext.facet#mention" && feature.DID != "" && feature.DID != actorDID {
				c.sendNotification(actorDID, feature.DID, "mention", "")
			}
		}
	}
}

func (c *Consumer) handleFollow(actorDID string, record json.RawMessage) {
	var follow FollowRecord
	if err := json.Unmarshal(record, &follow); err != nil {
		return
	}

	if follow.Subject == "" || follow.Subject == actorDID {
		return
	}

	c.sendNotification(actorDID, follow.Subject, "follow", "")
}

func (c *Consumer) handleBlockCreate(actorDID string, rkey string, record json.RawMessage) {
	var block BlockRecord
	if err := json.Unmarshal(record, &block); err != nil {
		return
	}

	if block.Subject == "" {
		return
	}

	// Only track blocks for registered DIDs
	if c.store.IsRegistered(actorDID) || c.store.IsRegistered(block.Subject) {
		c.store.AddBlock(actorDID, block.Subject, rkey)
		log.Printf("[jetstream] block: %s blocked %s (rkey=%s)", actorDID, block.Subject, rkey)
	}
}

func (c *Consumer) handleBlockDelete(actorDID string, rkey string) {
	if rkey == "" {
		return
	}

	blockedDID, err := c.store.RemoveBlockByRKey(actorDID, rkey)
	if err != nil {
		log.Printf("[jetstream] error removing block by rkey: %v", err)
		return
	}
	if blockedDID != "" {
		log.Printf("[jetstream] unblock: %s unblocked %s (rkey=%s)", actorDID, blockedDID, rkey)
	}
}

func (c *Consumer) sendNotification(actorDID, targetDID, notifType, subjectURI string) {
	if !c.store.IsRegistered(targetDID) {
		return
	}

	if c.store.IsBlocked(actorDID, targetDID) {
		log.Printf("[jetstream] suppressed %s notification: blocked (%s -> %s)", notifType, actorDID, targetDID)
		return
	}

	tokens, err := c.store.GetTokensForDID(targetDID)
	if err != nil {
		log.Printf("[jetstream] error getting tokens for %s: %v", targetDID, err)
		return
	}

	c.matchedEvents.Add(1)

	// Resolve actorDID to display name + handle for client-side formatting
	actorDisplayName := ""
	actorHandle := ""
	if c.profileResolver != nil {
		actorDisplayName, actorHandle = c.profileResolver.ResolveProfile(actorDID)
	}

	for _, token := range tokens {
		n := push.Notification{
			Token:    token.PushToken,
			Platform: token.Platform,
			Data: map[string]string{
				"type":             notifType,
				"actorDid":         actorDID,
				"actorDisplayName": actorDisplayName,
				"actorHandle":      actorHandle,
			},
		}
		if subjectURI != "" {
			n.Data["uri"] = subjectURI
		}

		if err := c.sender.Send(n); err != nil {
			c.pushErrors.Add(1)
			log.Printf("[jetstream] push error for %s: %v", targetDID, err)
		} else {
			c.pushesSent.Add(1)
		}
	}
}
