package jetstream

import (
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/gorilla/websocket"

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
	url    string
	store  *store.Store
	sender *push.MultiSender
}

func NewConsumer(url string, s *store.Store, sender *push.MultiSender) *Consumer {
	return &Consumer{
		url:    url,
		store:  s,
		sender: sender,
	}
}

func (c *Consumer) Run() {
	collections := []string{
		"app.bsky.feed.like",
		"app.bsky.feed.repost",
		"app.bsky.feed.post",
		"app.bsky.graph.follow",
		"app.bsky.graph.block",
	}

	params := "?"
	for i, col := range collections {
		if i > 0 {
			params += "&"
		}
		params += "wantedCollections=" + col
	}

	for {
		c.connect(c.url + params)
		log.Println("[jetstream] reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func (c *Consumer) connect(url string) {
	log.Printf("[jetstream] connecting to %s", url)

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		log.Printf("[jetstream] dial error: %v", err)
		return
	}
	defer conn.Close()

	log.Println("[jetstream] connected")

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[jetstream] read error: %v", err)
			return
		}

		var event Event
		if err := json.Unmarshal(message, &event); err != nil {
			continue
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
			c.handleBlockCreate(actorDID, commit.Record)
		} else if commit.Operation == "delete" {
			// For delete, we don't have the record anymore
			// Block deletes need to be handled differently
			// For now we skip — blocks accumulate but don't get removed via jetstream delete
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
	var like LikeRecord // same structure: subject.uri
	if err := json.Unmarshal(record, &like); err != nil {
		return
	}

	targetDID := extractDIDFromURI(like.Subject.URI)
	if targetDID == "" || targetDID == actorDID {
		return
	}

	c.sendNotification(actorDID, targetDID, "repost", like.Subject.URI)
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

func (c *Consumer) handleBlockCreate(actorDID string, record json.RawMessage) {
	var block BlockRecord
	if err := json.Unmarshal(record, &block); err != nil {
		return
	}

	if block.Subject == "" {
		return
	}

	// Only track blocks for registered DIDs
	if c.store.IsRegistered(actorDID) || c.store.IsRegistered(block.Subject) {
		c.store.AddBlock(actorDID, block.Subject)
		log.Printf("[jetstream] block: %s blocked %s", actorDID, block.Subject)
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

	titles := map[string]string{
		"like":    "New like",
		"repost":  "New repost",
		"reply":   "New reply",
		"mention": "You were mentioned",
		"quote":   "You were quoted",
		"follow":  "New follower",
	}

	for _, token := range tokens {
		n := push.Notification{
			Token:    token.PushToken,
			Platform: token.Platform,
			Title:    titles[notifType],
			Body:     actorDID, // TODO: resolve to display name
			Data: map[string]string{
				"type":     notifType,
				"actorDid": actorDID,
			},
		}
		if subjectURI != "" {
			n.Data["uri"] = subjectURI
		}

		if err := c.sender.Send(n); err != nil {
			log.Printf("[jetstream] push error for %s: %v", targetDID, err)
		}
	}
}
