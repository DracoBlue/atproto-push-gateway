# Notification Types

This document describes which ATproto record types trigger push notifications, with example Jetstream events and resulting push payloads.

## Implemented

### like

**Trigger:** `app.bsky.feed.like` record created

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.like",
    "rkey": "3kco5r7xsgb2p",
    "record": {
      "$type": "app.bsky.feed.like",
      "subject": {
        "uri": "at://did:plc:bob/app.bsky.feed.post/abc123",
        "cid": "bafyreiabc"
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.subject.uri` → authority part → `did:plc:bob`

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "like",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/abc123"
  }
}
```

---

### repost

**Trigger:** `app.bsky.feed.repost` record created

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.repost",
    "rkey": "3kco5r8abc",
    "record": {
      "$type": "app.bsky.feed.repost",
      "subject": {
        "uri": "at://did:plc:bob/app.bsky.feed.post/abc123",
        "cid": "bafyreiabc"
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.subject.uri` → authority part → `did:plc:bob`

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "repost",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/abc123"
  }
}
```

---

### reply

**Trigger:** `app.bsky.feed.post` record created with `reply` field

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.post",
    "rkey": "3kco5r9xyz",
    "record": {
      "$type": "app.bsky.feed.post",
      "text": "Great post!",
      "reply": {
        "parent": {
          "uri": "at://did:plc:bob/app.bsky.feed.post/abc123",
          "cid": "bafyreiabc"
        },
        "root": {
          "uri": "at://did:plc:bob/app.bsky.feed.post/root456",
          "cid": "bafyreiroot"
        }
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.reply.parent.uri` → authority part → `did:plc:bob`

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "reply",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/abc123"
  }
}
```

---

### mention

**Trigger:** `app.bsky.feed.post` record created with `facets` containing `app.bsky.richtext.facet#mention`

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.post",
    "rkey": "3kco5radef",
    "record": {
      "$type": "app.bsky.feed.post",
      "text": "Hey @bob check this out",
      "facets": [{
        "index": { "byteStart": 4, "byteEnd": 8 },
        "features": [{
          "$type": "app.bsky.richtext.facet#mention",
          "did": "did:plc:bob"
        }]
      }],
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.facets[].features[]` where `$type === "app.bsky.richtext.facet#mention"` → `did` field

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "mention",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:alice/app.bsky.feed.post/3kco5radef"
  }
}
```

Note: The `uri` is the mentioning post (actor's post), not the target's. This matches how Bluesky's `listNotifications` API returns mention notifications — the `uri` field points to the post containing the mention.

---

### quote

**Trigger:** `app.bsky.feed.post` record created with `embed.$type === "app.bsky.embed.record"` pointing to another user's post

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.post",
    "rkey": "3kco5rbghi",
    "record": {
      "$type": "app.bsky.feed.post",
      "text": "This is so true!",
      "embed": {
        "$type": "app.bsky.embed.record",
        "record": {
          "uri": "at://did:plc:bob/app.bsky.feed.post/abc123",
          "cid": "bafyreiabc"
        }
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.embed.record.uri` → authority part → `did:plc:bob`

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "quote",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/abc123"
  }
}
```

---

### follow

**Trigger:** `app.bsky.graph.follow` record created

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.graph.follow",
    "rkey": "3kco5rcjkl",
    "record": {
      "$type": "app.bsky.graph.follow",
      "subject": "did:plc:bob",
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Target DID extraction:** `record.subject` → `did:plc:bob`

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "follow",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

---

## Not Implemented

### like-via-repost

Someone liked a repost of your post. The Like's `subject.uri` points to the reposter's repost record, not directly to your post.

**Why not implemented:** Requires resolving the repost record via API to find the original post author. Expensive at high volume. Could be optimized by caching repost→original mappings from Jetstream events.

**Jetstream collection:** `app.bsky.feed.like` (same as regular like, but subject points to a repost)

### repost-via-repost

Someone reposted a repost of your post. Same multi-hop resolution problem as like-via-repost.

### subscribed-post (Bell Icon)

A user you subscribed to (via the bell icon) posted a new status.

**Why not implemented:** Activity subscriptions are private server-side data stored via bsync/stash. They are NOT stored in the user's AT Protocol repository and are NOT visible in the Jetstream. The gateway would need to:
1. Implement `app.bsky.notification.putActivitySubscription` XRPC endpoint
2. Store subscriptions in a local database
3. On every `app.bsky.feed.post` event, check if the author has subscribers
4. Fan out notifications to all subscribers

**Data model:**
```sql
CREATE TABLE activity_subscriptions (
  subscriber_did TEXT NOT NULL,
  subject_did TEXT NOT NULL,
  post BOOLEAN DEFAULT true,
  reply BOOLEAN DEFAULT false,
  PRIMARY KEY (subscriber_did, subject_did)
);
```

### starterpack-joined

Someone joined Bluesky via your starter pack.

**Jetstream collection:** Would need `app.bsky.graph.starterpack` — exact record structure and notification logic TBD.

### verified / unverified

Your account verification status changed.

**Jetstream collection:** `app.bsky.graph.verification` — record created (verified) or deleted (unverified).

### contact-match

A contact from your address book joined Bluesky.

**Why not implementable in gateway:** Requires access to the user's phone contacts, which is a client-side feature. The gateway has no way to know which phone numbers/emails correspond to which DIDs.

---

## Block Suppression

All notification types are suppressed if a block exists between the actor and the target (in either direction). Blocks are tracked in real-time via `app.bsky.graph.block` events from Jetstream.

**Jetstream Event (block created):**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.graph.block",
    "rkey": "3kco5rdmno",
    "record": {
      "$type": "app.bsky.graph.block",
      "subject": "did:plc:bob",
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

Block deletes are tracked by `rkey` to identify which block was removed.

## Mute Gap

Mutes are private and not available via Jetstream. Muted accounts may still trigger push notifications. This is a known limitation — the client can filter locally if needed.
