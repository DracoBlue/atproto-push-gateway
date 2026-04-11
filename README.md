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

| Event | Notification |
|---|---|
| Like | "X liked your post" |
| Repost | "X reposted your post" |
| Reply | "X replied to your post" |
| Mention | "X mentioned you" |
| Quote | "X quoted your post" |
| Follow | "X followed you" |

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
    "appId": "app.kiesel.Kiesel"
  }'

# 2. Check health
curl http://localhost:8080/health

# 3. The gateway is now listening on Jetstream.
#    When someone likes a post by the registered DID,
#    a push notification will be sent to the Expo Push Token.
```

### Docker

```bash
docker build -t atproto-push-gateway .
docker run -d \
  -p 8080:8080 \
  -v push-data:/data \
  -e DEV_MODE=true \
  -e EXPO_PUSH_ACCESS_TOKEN=your-token \
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
- **Push Delivery:** Expo Push API (FCM/APNs stubs ready for extension)
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
4. Verifies the ECDSA signature (ES256 P-256 fully supported, ES256K secp256k1 with graceful degradation)

### Display Name Resolution

Push notification bodies show display names ("Alice liked your post") instead of raw DIDs. Names are resolved via the public AppView API (`app.bsky.actor.getProfile`) and cached in memory (1 hour TTL, max 10,000 entries).

## Block Handling

The gateway maintains a real-time block graph:
- `app.bsky.graph.block` events consumed via Jetstream
- Before sending any push: bidirectional block check (has recipient blocked actor? has actor blocked recipient?)
- Blocks persisted in SQLite, loaded into memory on startup

**Note:** Mutes are private in ATproto and not available via Jetstream. Muted accounts may still trigger push notifications.

## Roadmap

- [ ] Full inter-service JWT verification (DID resolution + signature check)
- [ ] Direct FCM delivery (firebase-admin equivalent)
- [ ] Direct APNs delivery (HTTP/2 + .p8 key)
- [ ] Actor display name resolution (profile caching)
- [ ] Notification preferences (per-type toggles)
- [ ] Rate limiting per DID
- [ ] Web Push support
- [ ] Metrics endpoint (Prometheus)

## License

MIT — see [LICENSE](LICENSE)
