package jetstream

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"

	"github.com/dracoblue/atproto-push-gateway/internal/profile"
	"github.com/dracoblue/atproto-push-gateway/internal/push"
	"github.com/dracoblue/atproto-push-gateway/internal/store"
)

//go:embed zstd_dictionary
var zstdDictionary []byte

const (
	wsReadTimeout     = 60 * time.Second
	wsWriteTimeout    = 10 * time.Second
	wsPingInterval    = 20 * time.Second
	wsMaxMessageBytes = 1 << 20 // 1 MiB per frame
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

type StrongRef struct {
	URI string `json:"uri"`
	CID string `json:"cid"`
}

type LikeRecord struct {
	Subject StrongRef  `json:"subject"`
	Via     *StrongRef `json:"via,omitempty"`
}

type RepostRecord struct {
	Subject StrongRef  `json:"subject"`
	Via     *StrongRef `json:"via,omitempty"`
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

type VerificationRecord struct {
	Subject     string `json:"subject"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
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
		"app.bsky.graph.verification",
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

	conn.SetReadLimit(wsMaxMessageBytes)
	conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})

	// Ping loop in the background to keep the connection alive. Exits when
	// the connection is closed or the consumer is stopped. We wait for it
	// to exit before returning from connect() so the pinger can't race with
	// conn.Close(), which gorilla documents as safe-to-call-concurrently but
	// we'd still rather have a clean shutdown with no dangling WriteMessage.
	pingStop := make(chan struct{})
	var pingWG sync.WaitGroup
	pingWG.Add(1)
	defer func() {
		close(pingStop)
		pingWG.Wait()
	}()
	go func() {
		defer pingWG.Done()
		ticker := time.NewTicker(wsPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					log.Printf("[jetstream] ping error: %v", err)
					_ = conn.Close()
					return
				}
			case <-pingStop:
				return
			case <-c.stopCh:
				return
			}
		}
	}()

	// Create zstd decoder with Jetstream dictionary for compressed messages
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderDicts(zstdDictionary))
	if err != nil {
		log.Printf("[jetstream] failed to create zstd decoder: %v", err)
		return err
	}
	defer decoder.Close()

	log.Println("[jetstream] connected (zstd compression enabled, dictionary loaded)")

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
			c.handleLike(actorDID, commit.RKey, commit.Record)
		}
	case "app.bsky.feed.repost":
		if commit.Operation == "create" {
			c.handleRepost(actorDID, commit.RKey, commit.Record)
		}
	case "app.bsky.feed.post":
		if commit.Operation == "create" {
			c.handlePost(actorDID, commit.RKey, commit.Record)
		}
	case "app.bsky.graph.follow":
		if commit.Operation == "create" {
			c.handleFollow(actorDID, commit.RKey, commit.Record)
		}
	case "app.bsky.graph.block":
		if commit.Operation == "create" {
			c.handleBlockCreate(actorDID, commit.RKey, commit.Record)
		} else if commit.Operation == "delete" {
			c.handleBlockDelete(actorDID, commit.RKey)
		}
	case "app.bsky.graph.verification":
		if commit.Operation == "create" {
			c.handleVerificationCreate(actorDID, commit.RKey, commit.Record)
		} else if commit.Operation == "delete" {
			c.handleVerificationDelete(actorDID, commit.RKey)
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

func (c *Consumer) handleLike(actorDID string, rkey string, record json.RawMessage) {
	var like LikeRecord
	if err := json.Unmarshal(record, &like); err != nil {
		return
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.like/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "like", recordURI, like.Subject.URI)

	// like-via-repost: notify the reposter if discovered via their repost
	if like.Via != nil {
		reposterDID := extractDIDFromURI(like.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "like-via-repost", recordURI, like.Subject.URI)
		}
	}
}

func (c *Consumer) handleRepost(actorDID string, rkey string, record json.RawMessage) {
	var repost RepostRecord
	if err := json.Unmarshal(record, &repost); err != nil {
		return
	}

	targetDID := extractDIDFromURI(repost.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.feed.repost/%s", actorDID, rkey)
	c.sendNotification(actorDID, targetDID, "repost", recordURI, repost.Subject.URI)

	// repost-via-repost: notify the original reposter if discovered via their repost
	if repost.Via != nil {
		reposterDID := extractDIDFromURI(repost.Via.URI)
		if reposterDID != "" && reposterDID != actorDID && reposterDID != targetDID {
			c.sendNotification(actorDID, reposterDID, "repost-via-repost", recordURI, repost.Subject.URI)
		}
	}
}

func (c *Consumer) handlePost(actorDID string, rkey string, record json.RawMessage) {
	var post PostRecord
	if err := json.Unmarshal(record, &post); err != nil {
		return
	}

	postURI := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", actorDID, rkey)

	// Reply
	if post.Reply != nil {
		targetDID := extractDIDFromURI(post.Reply.Parent.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "reply", postURI, post.Reply.Parent.URI)
		}
	}

	// Quote
	if post.Embed != nil && post.Embed.Type == "app.bsky.embed.record" && post.Embed.Record != nil {
		targetDID := extractDIDFromURI(post.Embed.Record.URI)
		if targetDID != "" && targetDID != actorDID {
			c.sendNotification(actorDID, targetDID, "quote", postURI, post.Embed.Record.URI)
		}
	}

	// Mentions
	for _, facet := range post.Facets {
		for _, feature := range facet.Features {
			if feature.Type == "app.bsky.richtext.facet#mention" && feature.DID != "" && feature.DID != actorDID {
				c.sendNotification(actorDID, feature.DID, "mention", postURI, "")
			}
		}
	}
}

func (c *Consumer) handleFollow(actorDID string, rkey string, record json.RawMessage) {
	var follow FollowRecord
	if err := json.Unmarshal(record, &follow); err != nil {
		return
	}

	if follow.Subject == "" || follow.Subject == actorDID {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.graph.follow/%s", actorDID, rkey)
	c.sendNotification(actorDID, follow.Subject, "follow", recordURI, "")
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

func (c *Consumer) handleVerificationCreate(verifierDID string, rkey string, record json.RawMessage) {
	var verification VerificationRecord
	if err := json.Unmarshal(record, &verification); err != nil {
		return
	}

	if verification.Subject == "" {
		return
	}

	// Store for later deletion lookup
	c.store.AddVerification(verifierDID, verification.Subject, rkey)

	// Only notify registered DIDs
	if !c.store.IsRegistered(verification.Subject) {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.graph.verification/%s", verifierDID, rkey)
	c.sendNotification(verifierDID, verification.Subject, "verified", recordURI, "")
	log.Printf("[jetstream] verified: %s verified %s (rkey=%s)", verifierDID, verification.Subject, rkey)
}

func (c *Consumer) handleVerificationDelete(verifierDID string, rkey string) {
	if rkey == "" {
		return
	}

	subjectDID, err := c.store.RemoveVerificationByRKey(verifierDID, rkey)
	if err != nil {
		log.Printf("[jetstream] error removing verification by rkey: %v", err)
		return
	}

	if subjectDID == "" {
		return
	}

	if !c.store.IsRegistered(subjectDID) {
		return
	}

	recordURI := fmt.Sprintf("at://%s/app.bsky.graph.verification/%s", verifierDID, rkey)
	c.sendNotification(verifierDID, subjectDID, "unverified", recordURI, "")
	log.Printf("[jetstream] unverified: %s unverified %s (rkey=%s)", verifierDID, subjectDID, rkey)
}

// reasonTitles maps notification reasons to English titles.
// Clients can use the data fields to format localized text instead.
var reasonTitles = map[string]string{
	"like":              "New like",
	"repost":            "New repost",
	"reply":             "New reply",
	"mention":           "New mention",
	"quote":             "New quote",
	"follow":            "New follower",
	"like-via-repost":   "New like",
	"repost-via-repost": "New repost",
	"verified":          "Verified",
	"unverified":        "Verification removed",
}

// reasonBodyTemplates maps notification reasons to English body templates.
// %s is replaced with the actor's display name or handle.
var reasonBodyTemplates = map[string]string{
	"like":              "%s liked your post",
	"repost":            "%s reposted your post",
	"reply":             "%s replied to your post",
	"mention":           "%s mentioned you",
	"quote":             "%s quoted your post",
	"follow":            "%s followed you",
	"like-via-repost":   "%s liked a post you reposted",
	"repost-via-repost": "%s reposted a post you reposted",
	"verified":          "Your account has been verified",
	"unverified":        "Your account verification was removed",
}

func formatNotification(reason, actorDisplayName, actorHandle string) (string, string) {
	title := reasonTitles[reason]
	if title == "" {
		title = "Notification"
	}

	actorName := actorDisplayName
	if actorName == "" {
		actorName = actorHandle
	}
	if actorName == "" {
		actorName = "Someone"
	}

	template := reasonBodyTemplates[reason]
	if template == "" {
		return title, actorName
	}

	if reason == "verified" || reason == "unverified" {
		return title, template
	}

	return title, fmt.Sprintf(template, actorName)
}

func (c *Consumer) sendNotification(actorDID, targetDID, reason, recordURI, subjectURI string) {
	if !c.store.IsRegistered(targetDID) {
		return
	}

	if c.store.IsBlocked(actorDID, targetDID) {
		log.Printf("[jetstream] suppressed %s notification: blocked (%s -> %s)", reason, actorDID, targetDID)
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

	title, body := formatNotification(reason, actorDisplayName, actorHandle)

	for _, token := range tokens {
		n := push.Notification{
			Token:    token.PushToken,
			Platform: token.Platform,
			Title:    title,
			Body:     body,
			Data: map[string]string{
				"reason":           reason,
				"uri":              recordURI,
				"recipientDid":     targetDID,
				"actorDid":         actorDID,
				"actorDisplayName": actorDisplayName,
				"actorHandle":      actorHandle,
			},
		}
		if subjectURI != "" {
			n.Data["subject"] = subjectURI
		}

		if err := c.sender.Send(n); err != nil {
			c.pushErrors.Add(1)
			log.Printf("[jetstream] push error for %s: %v", targetDID, err)
		} else {
			c.pushesSent.Add(1)
		}
	}
}
