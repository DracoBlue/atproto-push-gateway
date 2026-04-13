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

Someone liked a post they discovered through a repost. The reposter gets a notification ("X liked a post you reposted").

**Jetstream Event:**
```json
{
  "did": "did:plc:alice",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.like",
    "rkey": "3l3qo2vuowo2b",
    "record": {
      "$type": "app.bsky.feed.like",
      "subject": {
        "uri": "at://did:plc:bob/app.bsky.feed.post/postid123",
        "cid": "bafyreiBOBSPOSTCID"
      },
      "via": {
        "uri": "at://did:plc:carol/app.bsky.feed.repost/repostid456",
        "cid": "bafyreiCAROLREPOSTCID"
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**How it works:** The `app.bsky.feed.like` record has an optional `via` field (a `strongRef`). When present, it points to the repost through which the user discovered the post. The `subject` always points to the **original post**, not the repost.

**Target DID extraction:** `record.via.uri` → authority part → `did:plc:carol` (the reposter)

**How to implement:**
1. In `handleLike`, check if `record.via` is present
2. If yes, extract DID from `via.uri` — that's the reposter who gets the `like-via-repost` notification
3. The existing `subject.uri` extraction still notifies the original author with a regular `like`
4. No API call needed — the `via` field contains everything

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "like-via-repost",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/postid123"
  }
}
```

---

### repost-via-repost

Someone reposted a post they discovered through another user's repost. The original reposter gets a notification.

**Jetstream Event:**
```json
{
  "did": "did:plc:dave",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.feed.repost",
    "rkey": "3l3qo2vuxyz2c",
    "record": {
      "$type": "app.bsky.feed.repost",
      "subject": {
        "uri": "at://did:plc:bob/app.bsky.feed.post/postid123",
        "cid": "bafyreiBOBSPOSTCID"
      },
      "via": {
        "uri": "at://did:plc:carol/app.bsky.feed.repost/repostid456",
        "cid": "bafyreiCAROLREPOSTCID"
      },
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**How it works:** Same pattern as `like-via-repost`. The `app.bsky.feed.repost` record also has an optional `via` field. The `subject` points to the original post, `via` points to the intermediary repost.

**Target DID extraction:** `record.via.uri` → authority part → `did:plc:carol` (the original reposter)

**How to implement:**
1. In `handleRepost`, check if `record.via` is present
2. If yes, extract DID from `via.uri` — that's the reposter who gets the `repost-via-repost` notification
3. The existing `subject.uri` extraction still notifies the original author with a regular `repost`
4. No API call needed — the `via` field contains everything

**Push Payload:**
```json
{
  "to": "<push-token>",
  "data": {
    "type": "repost-via-repost",
    "actorDid": "did:plc:dave",
    "actorDisplayName": "Dave",
    "actorHandle": "dave.bsky.social",
    "uri": "at://did:plc:bob/app.bsky.feed.post/postid123"
  }
}
```

---

### subscribed-post (Bell Icon)

A user you subscribed to (via the bell icon) posted a new status.

**Why not implementable via Jetstream:** Activity subscriptions are private server-side data stored via bsync/stash. They are NOT stored in the user's AT Protocol repository and are NOT visible in the Jetstream.

**Subscription API (server-side, not a record):**

`app.bsky.notification.putActivitySubscription` input:
```json
{
  "subject": "did:plc:someone",
  "activitySubscription": {
    "post": true,
    "reply": false
  }
}
```

`app.bsky.notification.listActivitySubscriptions` returns the user's subscriptions (authenticated).

**How to implement (if desired):**
1. Implement `app.bsky.notification.putActivitySubscription` XRPC endpoint on the gateway
2. Store subscriptions in SQLite:
   ```sql
   CREATE TABLE activity_subscriptions (
     subscriber_did TEXT NOT NULL,
     subject_did TEXT NOT NULL,
     post BOOLEAN DEFAULT true,
     reply BOOLEAN DEFAULT false,
     PRIMARY KEY (subscriber_did, subject_did)
   );
   ```
3. On every `app.bsky.feed.post` Jetstream event, check if the author has subscribers
4. Fan out `subscribed-post` notifications to all subscribers
5. Implement `listActivitySubscriptions` so the client can display current state

**Conclusion:** Implementable but requires the gateway to become stateful for subscriptions. The client must call `putActivitySubscription` against *our* gateway (not the AppView), and the gateway must index all `app.bsky.feed.post` events for subscribed authors. This is a significant architectural addition.

---

### starterpack-joined

Someone joined Bluesky via your starter pack.

**Starter pack record** (`app.bsky.graph.starterpack`):
```json
{
  "did": "did:plc:creator",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.graph.starterpack",
    "rkey": "3l3qo2vpackk",
    "record": {
      "$type": "app.bsky.graph.starterpack",
      "name": "Cool Bluesky People",
      "description": "A starter pack for newcomers",
      "list": "at://did:plc:creator/app.bsky.graph.list/listid123",
      "feeds": [
        { "uri": "at://did:plc:feedgen/app.bsky.feed.generator/techfeed" }
      ],
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Why not implementable via Jetstream:** The starter pack *record* is visible in Jetstream, but the *join event* is not. When a new user signs up through a starter pack link, the Bluesky server internally:
1. Creates the new account
2. Adds the user to the starter pack's underlying list
3. Synthesizes a `starterpack-joined` notification for the pack creator

There is no Jetstream event that says "user X joined via starter pack Y". The signup flow is entirely server-side.

**Conclusion:** Not implementable. The join event is a server-side synthesized notification with no corresponding Jetstream event.

---

### verified / unverified

Your account verification status changed.

**Jetstream Event (verified — record created):**
```json
{
  "did": "did:plc:verifier-authority",
  "kind": "commit",
  "commit": {
    "operation": "create",
    "collection": "app.bsky.graph.verification",
    "rkey": "3l3qo2vvvvv2c",
    "record": {
      "$type": "app.bsky.graph.verification",
      "subject": "did:plc:bob",
      "handle": "bob.bsky.social",
      "displayName": "Bob",
      "createdAt": "2026-04-11T12:00:00.000Z"
    }
  }
}
```

**Jetstream Event (unverified — record deleted):**
```json
{
  "did": "did:plc:verifier-authority",
  "kind": "commit",
  "commit": {
    "operation": "delete",
    "collection": "app.bsky.graph.verification",
    "rkey": "3l3qo2vvvvv2c"
  }
}
```

**Target DID extraction:** `record.subject` → `did:plc:bob`

**How to implement:**
1. Subscribe to `app.bsky.graph.verification` collection in Jetstream
2. On `create`: extract `record.subject` as target DID, send `verified` notification
3. On `delete`: would need to look up which DID was verified by that rkey (requires storing verification records)
4. Verification is only valid if issued by a trusted verifier AND current handle/displayName match — the gateway would need a list of trusted verifier DIDs

**Conclusion:** Implementable for `verified` (create events contain the target DID). The `unverified` (delete) case requires storing verification records to map rkey→subject DID. Additionally, the gateway should validate the verifier DID against a trusted list to avoid fake verification notifications.

---

### contact-match

A contact from your address book joined Bluesky.

**Why not implementable:** There is no Jetstream event. The notification is generated entirely server-side when a user imports phone contacts via `app.bsky.contact.importContacts`. The server matches contacts against known users and synthesizes notifications. The gateway has no way to know which phone numbers/emails correspond to which DIDs.

**Conclusion:** Not implementable in a push gateway. This is a server-side feature requiring access to private user data (contacts).

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
