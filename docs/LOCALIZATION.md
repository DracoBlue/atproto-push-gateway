# Localization

The gateway sends notification `title` and `body` in **English only**. Localization is intentionally a client-side concern.

## Why server-side localization is not implemented

Server-side localization would require either:

1. A `locale` field on every `registerPush` call, persisted per token. The gateway would then format `title`/`body` using a per-locale template map at dispatch time.
2. Translating templates per-language inside the gateway, plus a mechanism to refresh the stored locale every time the user changes their OS language.

Both options bind the user's *device* language to a value stored server-side at registration time. That value goes stale the moment the user switches their OS locale and never auto-refreshes. They also push translation maintenance (DE, FR, JA, …) into a Go service that has no other UI surface.

The client already knows the device locale at notification-display time. Doing the rewrite there is cheaper, always current, and keeps translations next to the rest of the app's i18n catalog.

## How clients are expected to localize

The English `title` and `body` shipped by the gateway are a **fallback for clients without a rewrite layer**. Clients that ship a Notification Service Extension (iOS) or a `FirebaseMessagingService` (Android) should:

1. Read the structured `data` fields (`reason`, `actorDisplayName`, `actorHandle`, `reasonSubject`, …) from the push payload.
2. Build a localized `title` + `body` from those fields using their own i18n catalog.
3. Overwrite the OS-displayed text before the notification is presented.

The `data` payload contains everything needed to do this without an API roundtrip — see [`NOTIFICATIONS.md`](./NOTIFICATIONS.md) for the field list.

### Required iOS behavior

The gateway sets `mutable-content: 1` on every APNs payload (both direct APNs and via Expo Push). An NSE in the client app will be invoked automatically and can rewrite `bestAttempt.title` / `bestAttempt.body` before calling `contentHandler`.

### Required Android behavior

⚠️ **Current FCM payload includes a `notification` block.** When the app is backgrounded, Android renders that block directly via the OS and never wakes the app — so the client cannot intercept. To allow client-side rewriting on Android, the gateway must send a **data-only** message (no `notification` field) so the registered `FirebaseMessagingService` fires for every push, regardless of foreground state.

Options for evolving the Android behavior:

- **Always data-only on Android.** Simpler, but breaks any client that relies on the OS-rendered fallback. Acceptable if the gateway is only consumed by clients that ship a `FirebaseMessagingService`.
- **Per-registration capability flag.** Add a `clientFormats: true` field to `registerPush`. When set, the gateway omits the `notification` block. Clients without a rewrite layer keep the current behavior. This is the recommended path if a heterogeneous client ecosystem is expected.

Neither change has been implemented yet.

## Prior art

The Bluesky social-app (`bsky.app`) does **not** localize push notifications either — title/body are always English on both iOS and Android. Their NSE only mutates badge count, chat sound, and communication-notification metadata; their Android `BackgroundNotificationHandler` only sets the channel ID. This gateway's stance is the same as theirs, with the explicit option for downstream clients to do better.

## Field stability

Clients localize against `data.reason` values and the structured author/subject fields. Those identifiers (`like`, `repost`, `reply`, `mention`, `quote`, `follow`, `like-via-repost`, `repost-via-repost`, `verified`, `unverified`) and field names (`actorDid`, `actorDisplayName`, `actorHandle`, `recipientDid`, `uri`, `subject`, `reasonSubject`) are part of the gateway's stable contract — they will not be renamed without a version bump. New reasons will be added with new identifiers; existing ones will not change semantics.

The English `title` and `body` strings are **not** considered stable. They may be reworded for clarity at any time. Clients that depend on them verbatim (rather than treating them as a fallback) will break.
