# Fediverse Web Push Relay

Relays [RFC 8030](https://www.rfc-editor.org/rfc/rfc8030) Web Push deliveries
from Mastodon-compatible servers to APNs and FCM, so a mobile app can receive
Fediverse notifications without the server knowing anything about mobile push.

Works with any server speaking the Mastodon client API: Mastodon, Pleroma /
Akkoma, GoToSocial, Iceshrimp, Pixelfed.

## Why a relay and not a gateway

The ATproto side of this project is a **watcher**: it consumes Jetstream, sees
events, decides what is notification-worthy, and composes the payload itself.

Mastodon has no equivalent firehose for notifications. It **pushes** — it
encrypts a notification for a subscriber's public key and POSTs the ciphertext
to whatever URL the subscriber registered. So the shape here is reversed: the
relay holds a URL open and forwards what arrives.

In the browser, that URL belongs to Google's or Mozilla's push service, which
forwards to the browser and never sees plaintext. This relay takes exactly
that role, with APNs/FCM as the last mile instead of a browser connection.
To the Fediverse server the two are indistinguishable — the protocol only
knows `endpoint` as an arbitrary URL.

```
Mastodon ──ciphertext──> relay ──base64──> APNs/FCM ──> NSE ──> decrypted
```

## It cannot read what it carries

The subscriber's private key never leaves the device, so the relay forwards an
opaque blob. That is not merely good hygiene. A Mastodon web push payload
contains the user's **OAuth access token**:

```ruby
# app/serializers/web/notification_serializer.rb
attributes :access_token, :preferred_locale, :notification_id,
           :notification_type, :icon, :title, :body
```

A relay that decrypted would hold account-level credentials for every user it
serves. This one holds a device token and nothing else.

A useful side effect: because `access_token` travels inside the payload, the
device can call `/api/v1/notifications/:id` itself after decrypting — for the
status URI needed to deep-link, the full text, or the avatar.

## Status codes are a security control

Mastodon's `Web::PushNotificationWorker` destroys the subscription on any 4xx
except 408 and 429:

```ruby
if (400..499).cover?(response.code) && ![408, 429].include?(response.code)
  @subscription.destroy!
elsif !(200...300).cover?(response.code)
  raise Mastodon::UnexpectedResponseError, response
end
```

There is no retry. A 4xx therefore does not mean "this delivery failed", it
means **"cancel this subscription permanently"** — and the user silently stops
receiving notifications until the app re-registers.

The consequence shapes the whole handler. If a failed VAPID check answered
403, then anyone who learned an endpoint URL could destroy that user's
subscription with one forged request — strictly worse than the spam the check
exists to prevent. So rejections are accepted and dropped instead.

| Condition | Status | Rationale |
|---|---|---|
| Delivered to APNs/FCM | `201` | — |
| Endpoint id unknown | `404` | The only legitimate cancellation: it really is gone |
| Device token permanently invalid | `404` | No recipient remains; mapping is dropped too |
| VAPID check failed | `202` | Drop silently — a 4xx would be a subscription-kill primitive |
| Body empty, oversized, or legacy-encoded | `202` | Undeliverable, but the subscription is fine |
| Rate limit hit | `429` | In Mastodon's exemption list, so it retries |
| Store or delivery failure | `5xx` | Transient; the sender should retry |

`GET /relay/{id}` answers `405`, not `404`, so a sender probing an endpoint
does not destroy the subscription.

The same reasoning makes the origin-verify exemption unconditional. Requests
arrive from arbitrary Fediverse servers, which cannot attach the shared-secret
header, so `/relay/` is always exempt in
`internal/originverify/middleware.go` — not an operator toggle, because
getting it wrong is silent and permanent.

## Authentication: pinned VAPID keys

Two things protect an endpoint.

**The URL is a bearer credential.** 32 random bytes, so it cannot be guessed
or enumerated. Only its SHA-256 hash is stored, so a leaked database does not
hand out working endpoints.

**The instance's VAPID key is pinned.** Verifying the [RFC
8292](https://www.rfc-editor.org/rfc/rfc8292) JWT alone proves nothing —
anyone can mint a key pair and sign a valid token. The guarantee appears only
when the offered `k` is compared against a key pinned out of band. The client
reads its own instance's key at registration:

```
GET /api/v2/instance → configuration.vapid.public_key
```

and passes it to `/relay/register`. The check then means "only the instance
this subscription belongs to may push here".

Note `v2`: current Mastodon no longer reports `vapid_key` from
`/api/v1/instance`.

Registering without `vapidPublicKey` leaves the endpoint open to anyone who
learns the URL. It is supported for bringing a deployment up and logs a
warning on every push.

A content-level guarantee backs this up: AES-GCM is authenticated encryption,
so a successful decryption on the device proves the sender held the shared
secret. An attacker who has the URL but not the subscriber's `p256dh` public
key cannot produce anything the device will accept.

### What a forged push can still do

One residual effect, which is why relay-side checks must stay strict: iOS
needs a non-empty alert plus `mutable-content` to launch the Notification
Service Extension, and a `UNNotificationServiceExtension` **cannot suppress a
notification once delivered** — it may replace content, not cancel it. A push
the device fails to decrypt therefore surfaces as the bare placeholder title.
Pinning is what keeps that from being reachable.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `RELAY_ENABLED` | `false` | Set to `true` to run the relay. |
| `FCM_DATA_ONLY` | `false` | Must be `true` whenever the relay is enabled. |

The relay has no origin setting of its own. The endpoint URLs handed to
clients are built from `PUSH_GATEWAY_DID`, using the same derivation as the
DID document's `serviceEndpoint` — `did:web:push.example.org` becomes
`https://push.example.org`. The gateway's origin is therefore stated in one
place, and the relay cannot end up advertising a host the ATproto side does
not serve.

This means the relay must answer on the gateway's own DID host. Running it
elsewhere would need a separate deployment with its own
`PUSH_GATEWAY_DID`.

**The relay refuses to start without `FCM_DATA_ONLY=true`** — the process
exits with an explanatory error rather than serving.

The reason is that the relay forwards ciphertext only the device can read, so
on Android it has to arrive as a data message. A notification message would be
rendered by the OS as the bare placeholder title, and the
`FirebaseMessagingService` holding the decryption key would never be woken:
every Android user would get a stream of contentless notifications while the
ciphertext sat unused in the payload.

Coupling the two settings is deliberate. The alternative — forcing data-only
per message and leaving the global setting alone — would make the relay work
under a configuration whose stated behaviour it contradicts. Failing loudly at
startup is easier to diagnose than notifications that arrive empty.

An iOS-only deployment must still set it; with no FCM sender configured the
setting has no other effect.

`GET /health` reports `relayEndpoints` alongside the ATproto counters.

## Endpoints

### `POST /relay/register`

```json
{
  "deviceToken": "<APNs or FCM token>",
  "platform": "ios",
  "appId": "app.example.MyApp",
  "vapidPublicKey": "BCk-QqERU0q-CfYZjcuB6lny...",
  "instance": "mastodon.social"
}
```

→ `201`

```json
{ "endpoint": "https://push.example.org/relay/kJ8vQ2…" }
```

Re-registering the same `deviceToken` + `appId` replaces the previous
endpoint, so a device always has exactly one.

`instance` is stored for operator diagnostics only and never used for
authorization.

### `POST /relay/{id}`

Called by the Fediverse server. Body is the `aes128gcm` ciphertext,
authenticated by the VAPID `Authorization` header. Forwarded as:

```json
{
  "aps": { "alert": { "title": "Fediverse" }, "mutable-content": 1 },
  "data": { "m": "<base64url ciphertext>", "source": "webpush" }
}
```

Android delivery relies on `FCM_DATA_ONLY`, which the relay requires — see
Configuration.

### `POST /relay/unregister`

Either `{"endpoint": "<full URL>"}` or `{"deviceToken": "...", "appId": "..."}`
to clear everything for a device, which is what a logout path wants. → `204`

## Client responsibilities

The relay is the small half. A client has to:

1. **Generate a P-256 key pair and a 16-byte auth secret.** Store the private
   key where the decrypting code can reach it — on iOS that means an App
   Group, not just the Keychain, because the NSE runs in a different process.
   Store the instance host alongside it.

2. **Register with the relay**, then subscribe at the instance:

   ```
   POST /api/v1/push/subscription
     subscription[endpoint]      = <endpoint from /relay/register>
     subscription[keys][p256dh]  = <base64url public key>
     subscription[keys][auth]    = <base64url 16-byte secret>
     subscription[standard]      = true
     data[alerts][mention]       = true
     data[alerts][favourite]     = …
     data[policy]                = all | followed | follower | none
   ```

   `standard=true` matters: without it Mastodon uses the legacy `aesgcm`
   encoding, which carries salt and server key in separate headers rather than
   in the body. The relay drops those, because forwarding the body alone would
   give the device something undecryptable.

3. **Decrypt on device** per [RFC
   8291](https://www.rfc-editor.org/rfc/rfc8291): ECDH → HKDF-SHA256 →
   AES-128-GCM. iOS has all of it in CryptoKit (`P256.KeyAgreement`, `HKDF`,
   `AES.GCM`); Android has P-256 ECDH in the standard JCE.

4. **Re-register when the device token changes**, and unregister on logout —
   both at the relay and via `DELETE /api/v1/push/subscription`.

### Key rotation

If an instance rotates its VAPID key, pushes start failing the pinned check
and are dropped silently — no notifications, no error visible to the user. The
relay logs every mismatch. Clients should re-read
`configuration.vapid.public_key` periodically (app start is enough) and
re-register when it differs.

## Observed wire format

Measured against mastodon.social (4.8.0-nightly, 2026-10-09) by subscribing a
local capture endpoint and decrypting a real `mention` notification.

```
Content-Encoding: aes128gcm
Content-Type:     application/octet-stream
TTL:              172800
Urgency:          normal
Unsubscribe-URL:  present
Authorization:    vapid t=<JWT>,k=<65-byte key>
  claims: {"aud":"<endpoint origin>","exp":<unix>,"sub":"mailto:staff@mastodon.social"}
record size:      4096
```

The VAPID header matches what `internal/vapid` parses: the `vapid` scheme
word, parameters `t` and `k`, and an uncompressed 65-byte P-256 point. `aud`
is the endpoint's origin; the relay does not check it, since behind a proxy
the origin it would compare against is not reliably knowable.

### Payload size

| | |
|---|---|
| Observed `mention` ciphertext | 376 B |
| → APNs payload | ~622 B of 4096 (15%) |
| Computed worst case (every field maximal, 140 multibyte chars in `body`) | 707 B ciphertext, ~1063 B payload (26%) |
| `maxCiphertextBytes` ceiling | 2800 B |

`body` is truncated to 140 characters upstream, so the worst case is bounded
and the ceiling cannot be reached by a well-behaved sender. Oversized payloads
are dropped with a log line rather than truncated, so a future format change
fails visibly instead of corruptly.

### Two details that affect client implementations

**`notification_id` is a JSON number, not a string.**

```json
{"notification_id": 629773749, "notification_type": "mention", ...}
```

It is Rails' `object.id`, an integer. Decoding it into a `String` field will
fail — relevant for both the Swift and Kotlin structs.

**`title` and `body` arrive already localized.** Mastodon renders them server
side under the subscription's locale, which the payload reports as
`preferred_locale`:

```json
{"preferred_locale": "de", "title": "Eubecio Insaboth erwähnte dich",
 "body": "dear @dracoblue"}
```

This is the opposite of the ATproto side of this gateway, which sends English
defaults and expects the client to localize from the `data` fields. A client
handling relayed Fediverse pushes should display `title`/`body` as received
and must not map `notification_type` through its own translation table — doing
so would replace the server's correct localization with a second-guess.
