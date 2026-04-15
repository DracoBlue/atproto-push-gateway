# atproto-push-gateway

A self-hosted push notification gateway for [AT Protocol](https://atproto.com/) apps. Receives `registerPush` calls from any PDS and delivers native push notifications (FCM/APNs/Expo) when social events occur.

## Why?

Bluesky's push infrastructure (`push.bsky.app`) is closed source and does not send push notifications to third-party apps. If you build your own ATproto client, you need your own push gateway. This project fills that gap.

## How It Works

```
Your App                      PDS (user's)              push.example.org
    │                            │                            │
    │─── registerPush ──────────>│                            │
    │    serviceDid:             │─── XRPC forward ──────────>│
    │    did:web:push.example.org   │    + Service-Auth JWT       │
    │                            │                            │── store token in SQLite
    │                            │                            │
    │                            │                    Jetstream│
    │                            │                   (WebSocket)
    │                            │                            │── match event to DID
    │                            │                            │── check block graph
    │                            │                            │── construct payload
    │<────────── Push ───────────┼────────────────────────────│
```

The gateway:
1. **Registers tokens** via the standard `app.bsky.notification.registerPush` XRPC endpoint
2. **Listens to Jetstream** for real-time events (likes, replies, reposts, follows, mentions, quotes)
3. **Matches events** against registered DIDs using an in-memory hashmap (O(1) lookup)
4. **Checks blocks** in real-time (block graph maintained via Jetstream)
5. **Delivers push notifications** via Expo Push API, FCM, or APNs

## Supported Events

| Event | Default Title | Default Body |
|---|---|---|
| Like | New like | X liked your post |
| Repost | New repost | X reposted your post |
| Reply | New reply | X replied to your post |
| Mention | New mention | X mentioned you |
| Quote | New quote | X quoted your post |
| Follow | New follower | X followed you |
| Like via repost | New like | X liked a post you reposted |
| Repost via repost | New repost | X reposted a post you reposted |
| Verified | Verified | Your account has been verified |
| Unverified | Verification removed | Your account verification was removed |

### Push Payload

The gateway sends English `title` and `body` as defaults, plus structured `data` fields for client-side localization. Clients with an iOS Notification Service Extension or Android background handler can use the `data` fields to format localized text and override the defaults. `mutableContent: true` tells iOS to invoke the NSE before display.

```json
{
  "to": "ExponentPushToken[...]",
  "title": "New like",
  "body": "Alice liked your post",
  "sound": "default",
  "mutableContent": true,
  "data": {
    "reason": "like",
    "uri": "at://did:plc:alice/app.bsky.feed.like/3kco5r7x",
    "subject": "at://did:plc:bob/app.bsky.feed.post/abc123",
    "recipientDid": "did:plc:bob",
    "actorDid": "did:plc:alice",
    "actorDisplayName": "Alice",
    "actorHandle": "alice.bsky.social"
  }
}
```

| Data Field | Description |
|---|---|
| `reason` | Notification type (like, repost, reply, mention, quote, follow, like-via-repost, repost-via-repost, verified, unverified) |
| `uri` | AT-URI of the record that caused the notification |
| `subject` | AT-URI of the acted-upon post (present for like, repost, reply, quote, like-via-repost, repost-via-repost) |
| `recipientDid` | DID of the recipient (for multi-account routing) |
| `actorDid` | DID of the actor who performed the action |
| `actorDisplayName` | Actor's display name (may be empty) |
| `actorHandle` | Actor's handle (may be empty) |

## Quick Start

### Local Development

```bash
# Clone
git clone https://github.com/DracoBlue/atproto-push-gateway.git
cd atproto-push-gateway

# Run in dev mode (no JWT verification required)
DEV_MODE=true go run ./cmd/server
```

The gateway starts on port 8080, connects to Jetstream, and serves:
- `POST /xrpc/app.bsky.notification.registerPush` — Token registration
- `POST /xrpc/app.bsky.notification.unregisterPush` — Token removal
- `GET /.well-known/did.json` — DID document for service discovery
- `GET /health` — Health check with stats

In dev mode, additional test endpoints are available:
- `POST /test/register` — Register a token without JWT auth
- `POST /test/push` — Check registered tokens for a DID

### Test It

```bash
# 1. Register a test token (dev mode only)
curl -X POST http://localhost:8080/test/register \
  -H "Content-Type: application/json" \
  -d '{
    "actorDid": "did:plc:your-did-here",
    "token": "ExponentPushToken[xxxxxx]",
    "platform": "ios",
    "appId": "org.example.app"
  }'

# 2. Check health
curl http://localhost:8080/health

# 3. The gateway is now listening on Jetstream.
#    When someone likes a post by the registered DID,
#    a push notification will be sent to the Expo Push Token.
```

### Docker (GHCR)

Pre-built images are available on GitHub Container Registry:

```bash
docker pull ghcr.io/dracoblue/atproto-push-gateway:latest
```

```bash
docker run -d \
  -p 8080:8080 \
  -v push-data:/data \
  -e PUSH_GATEWAY_DID=did:web:push.example.org \
  -e EXPO_PUSH_ACCESS_TOKEN=your-token \
  ghcr.io/dracoblue/atproto-push-gateway:latest
```

With direct APNs + FCM:

```bash
docker run -d \
  -p 8080:8080 \
  -v push-data:/data \
  -e PUSH_GATEWAY_DID=did:web:push.example.org \
  -e APNS_KEY_BASE64=LS0tLS1CRUdJTi... \
  -e APNS_KEY_ID=ABC123DEF4 \
  -e APNS_TEAM_ID=TEAMID1234 \
  -e APNS_TOPIC=org.example.app \
  -e FCM_SERVICE_ACCOUNT_BASE64=eyJ0eXBlIjoic2Vydm... \
  ghcr.io/dracoblue/atproto-push-gateway:latest
```

### Build from Source

```bash
docker build -t atproto-push-gateway .
docker run -d \
  -p 8080:8080 \
  -v push-data:/data \
  -e DEV_MODE=true \
  atproto-push-gateway
```

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `PUSH_GATEWAY_DID` | `did:web:localhost` | Your service DID (e.g. `did:web:push.example.org`) |
| `PUSH_GATEWAY_PORT` | `8080` | HTTP server port |
| `SQLITE_PATH` | `./push-gateway.db` | Path to SQLite database file |
| `JETSTREAM_URL` | `wss://jetstream2.us-east.bsky.network/subscribe` | Jetstream WebSocket URL |
| `EXPO_PUSH_ACCESS_TOKEN` | (empty) | Expo Push API access token |
| `DEV_MODE` | (empty) | Set to `true` to enable test endpoints and skip JWT verification |
| `APNS_KEY_PATH` | (empty) | Path to APNs .p8 key file (for direct APNs delivery) |
| `APNS_KEY_BASE64` | (empty) | Base64-encoded APNs .p8 key (alternative to file path) |
| `APNS_KEY_ID` | (empty) | APNs Key ID (from Apple Developer Portal) |
| `APNS_TEAM_ID` | (empty) | Apple Developer Team ID |
| `APNS_TOPIC` | (empty) | APNs topic / iOS bundle ID (e.g. `org.example.app`) |
| `APNS_SANDBOX` | (empty) | Set to `true` for APNs sandbox (dev/preview builds) |
| `FCM_SERVICE_ACCOUNT_PATH` | (empty) | Path to Firebase service account JSON (for direct FCM delivery) |
| `FCM_SERVICE_ACCOUNT_BASE64` | (empty) | Base64-encoded service account JSON (alternative to file path) |

## Production Setup

### 1. Create a DID Document

Host `/.well-known/did.json` on your domain:

```json
{
  "@context": ["https://www.w3.org/ns/did/v1"],
  "id": "did:web:push.example.org",
  "service": [{
    "id": "#bsky_notif",
    "type": "BskyNotificationService",
    "serviceEndpoint": "https://push.example.org"
  }]
}
```

The gateway serves this automatically based on `PUSH_GATEWAY_DID`.

### 2. Configure Your App

In your ATproto client, call `registerPush` with your gateway's DID:

```typescript
agent.app.bsky.notification.registerPush({
  serviceDid: 'did:web:push.example.org',
  token: devicePushToken,
  platform: 'ios', // or 'android'
  appId: 'org.example.app',
}, {
  headers: {
    'atproto-proxy': 'did:web:push.example.org#bsky_notif',
  },
});
```

### 3. Deploy with TLS

The service must be reachable via HTTPS (required for DID document resolution and PDS forwarding). Use a reverse proxy (nginx/caddy) with Let's Encrypt.

## Architecture

- **Language:** Go
- **Database:** SQLite (single file, no external DB server)
- **Event Source:** [Jetstream](https://github.com/bluesky-social/jetstream) with zstd compression
- **Push Delivery:** Direct APNs (HTTP/2 + .p8), Direct FCM (v1 API + OAuth2), Expo Push API (fallback)
- **In-Memory:** Hashmap of registered DIDs + block graph for fast matching
- **Single process, single container, no external services**

### Why Not Use Bluesky's Push Service?

Bluesky's push infrastructure (`push.bsky.app`) is closed source and **does not send push notifications to third-party apps**. This was confirmed by Bluesky engineer pfrazee in [GitHub Discussion #1914](https://github.com/bluesky-social/atproto/discussions/1914): *"Bluesky will not send push notifications to 3rd parties. You have to setup your own backend to do that."*

The `registerPush` call succeeds (returns 200 OK) because the PDS stores the token, but the push delivery service at `push.bsky.app` only has the APNs/FCM certificates for `xyz.blueskyweb.app` — it cannot push to your app's bundle ID.

### How the ATproto Push Chain Works

```
Client App → PDS (proxy) → AppView (api.bsky.app)
                                    ↓
                              push.bsky.app ← CLOSED SOURCE
                                    ↓
                              APNs / FCM → Device (Bluesky app only)
```

This gateway replaces `push.bsky.app` with your own service:

```
Client App → PDS (proxy) → YOUR push gateway (push.example.org)
                                    ↓
                              Jetstream (event detection)
                                    ↓
                              APNs / FCM / Expo → Device (YOUR app)
```

### Jetstream Bandwidth

The gateway subscribes to [Jetstream](https://github.com/bluesky-social/jetstream) instead of the raw firehose:

| Mode | Bandwidth/Day | Factor |
|---|---|---|
| Raw Firehose (CBOR/CAR) | ~232 GB | Baseline |
| Jetstream uncompressed (JSON) | ~5-10 GB | ~25x smaller |
| **Jetstream + zstd** (this gateway) | **~850 MB** | ~270x smaller |

zstd compression reduces bandwidth by ~85-90% vs uncompressed JSON. CPU overhead for decompression is minimal (~1-2% of a core at full stream).

### Lazy Start

The Jetstream connection is only established when the first push token is registered. Until then, zero bandwidth is consumed. On restart, if tokens exist in SQLite, the connection starts immediately.

### JWT Verification

The PDS forwards `registerPush` calls with an inter-service JWT signed by the user's identity key. This gateway:

1. Decodes the JWT and validates claims (`iss`, `aud`, `lxm`, `exp`)
2. Resolves the issuer DID (`did:plc` via plc.directory, `did:web` via .well-known/did.json)
3. Extracts the `#atproto` signing key from the DID document
4. Verifies the ECDSA signature (ES256 P-256 and ES256K secp256k1 fully supported)

### Display Name Resolution

Push notification bodies show display names ("Alice liked your post") instead of raw DIDs. Names are resolved via the public AppView API (`app.bsky.actor.getProfile`) and cached in memory (1 hour TTL, max 10,000 entries).

## Block Handling

The gateway maintains a real-time block graph:
- `app.bsky.graph.block` events consumed via Jetstream
- Before sending any push: bidirectional block check (has recipient blocked actor? has actor blocked recipient?)
- Blocks persisted in SQLite, loaded into memory on startup

**Note:** Mutes are private in ATproto and not available via Jetstream. Muted accounts may still trigger push notifications.

## Client-Side Localization

The gateway sends English `title` and `body` as defaults. Clients can override these with localized text using the `data` fields before the notification is displayed.

### iOS: Notification Service Extension (NSE)

iOS apps can add a [Notification Service Extension](https://developer.apple.com/documentation/usernotifications/modifying-content-in-newly-delivered-notifications) that intercepts push notifications before display. The NSE reads `reason`, `actorDisplayName`, and `actorHandle` from the payload's `data` dictionary and sets localized `title` and `body`.

Requirements:
- `mutableContent: true` in the payload (set by the gateway)
- A non-empty `title` and `body` in the APNs alert (the gateway sends English defaults)
- An NSE target in your Xcode project

Example NSE logic (Swift):

```swift
let reason = userInfo["reason"] as? String ?? ""
let actor = userInfo["actorDisplayName"] as? String ?? "Someone"

switch reason {
case "like":
    bestAttempt.title = "Neuer Like"       // German
    bestAttempt.body = "\(actor) hat deinen Beitrag geliked"
case "follow":
    bestAttempt.title = "Neuer Follower"
    bestAttempt.body = "\(actor) folgt dir jetzt"
// ... other reasons
default:
    break // keep English defaults from gateway
}
```

The NSE has ~30 seconds to modify the notification. If it times out, iOS displays the original English text.

### Android: Background Handler

Android apps can use a background message handler (e.g. via `expo-notifications` or Firebase's `onMessageReceived`) to modify notification content before display. The `data` fields are available in the message payload.

The gateway sets `android.notification.channel_id` to the `reason` value, so users can configure per-type notification settings (sound, vibration, importance) in Android system settings.

### Data Fields for Localization

| Field | Example | Use |
|---|---|---|
| `reason` | `like` | Determines notification template |
| `actorDisplayName` | `Alice` | Actor's display name (preferred) |
| `actorHandle` | `alice.bsky.social` | Actor's handle (fallback if no display name) |

Supported `reason` values: `like`, `repost`, `reply`, `mention`, `quote`, `follow`, `like-via-repost`, `repost-via-repost`, `verified`, `unverified`

## Roadmap

- [x] Full inter-service JWT verification (DID resolution + signature check)
- [x] Actor display name resolution (profile caching, 1h TTL)
- [x] like-via-repost / repost-via-repost (via field)
- [x] verified / unverified (app.bsky.graph.verification)
- [x] Payload aligned with [bluesky's social-app](https://github.com/bluesky-social/social-app) conventions (reason/uri/subject/recipientDid)
- [x] zstd dictionary compression for Jetstream
- [x] mutableContent support for iOS Notification Service Extension
- [x] Direct APNs delivery (HTTP/2 + .p8 key, JWT auth with auto-refresh)
- [x] Direct FCM delivery (v1 API + OAuth2 service account)
- [ ] Block list support (app.bsky.graph.listblock — resolve list membership)
- [ ] Rate limiting per DID
- [ ] Web Push support
- [ ] Metrics endpoint (Prometheus)
- [x] secp256k1 full signature verification (via decred/dcrd)

## License

MIT — see [LICENSE](LICENSE)
