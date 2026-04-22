package xrpc

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

// backfillClient is a shared HTTP client for public AppView fetches.
var backfillClient = &http.Client{Timeout: 10 * time.Second}

// backfillBlocks fetches historical block records from the public AppView for a
// given DID and seeds them into the store. Runs asynchronously. If a fetch
// error occurs mid-pagination, the function logs and returns early — blocks
// already seeded in earlier pages remain in the store, and the DID stays
// marked as backfilled (no automatic retry). Blocks seen after the mark
// via Jetstream still land via the live path.
func (h *Handler) backfillBlocks(actorDID string) {
	const endpoint = "https://public.api.bsky.app/xrpc/com.atproto.repo.listRecords"
	const maxPages = 20 // safety: 100 records/page * 20 = 2000 blocks max
	cursor := ""
	total := 0
	for page := 0; page < maxPages; page++ {
		u, _ := url.Parse(endpoint)
		q := u.Query()
		q.Set("repo", actorDID)
		q.Set("collection", "app.bsky.graph.block")
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		u.RawQuery = q.Encode()

		resp, err := backfillClient.Get(u.String())
		if err != nil {
			log.Printf("[blocks-backfill] %s: %v", actorDID, err)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("[blocks-backfill] %s: HTTP %d", actorDID, resp.StatusCode)
			return
		}

		var out struct {
			Records []struct {
				URI   string `json:"uri"`
				Value struct {
					Subject string `json:"subject"`
				} `json:"value"`
			} `json:"records"`
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			log.Printf("[blocks-backfill] %s: parse: %v", actorDID, err)
			return
		}

		for _, rec := range out.Records {
			if rec.Value.Subject == "" {
				continue
			}
			rkey := uriRKey(rec.URI)
			if rkey == "" {
				log.Printf("[blocks-backfill] %s: skipping record with empty rkey (uri=%q)", actorDID, rec.URI)
				continue
			}
			if err := h.store.AddBlock(actorDID, rec.Value.Subject, rkey); err != nil {
				log.Printf("[blocks-backfill] %s: add: %v", actorDID, err)
				continue
			}
			total++
		}

		if out.Cursor == "" {
			break
		}
		cursor = out.Cursor
	}

	log.Printf("[blocks-backfill] %s: seeded %d existing blocks", actorDID, total)
}

// uriRKey returns the last path segment of an at:// URI, which is the rkey.
func uriRKey(uri string) string {
	for i := len(uri) - 1; i >= 0; i-- {
		if uri[i] == '/' {
			return uri[i+1:]
		}
	}
	return ""
}

// maybeStartBlocksBackfill claims the backfill for this DID via
// MarkBlocksBackfilled and runs it asynchronously if claimed. The mark is
// written before the backfill completes, so a mid-flight crash leaves the DID
// marked without a completed backfill — acceptable at this scale since blocks
// seen via Jetstream after the mark will still land via the live path.
func (h *Handler) maybeStartBlocksBackfill(actorDID string) {
	claimed, err := h.store.MarkBlocksBackfilled(actorDID)
	if err != nil {
		log.Printf("[blocks-backfill] claim error: %v", err)
		return
	}
	if !claimed {
		return
	}
	go h.backfillBlocks(actorDID)
}
