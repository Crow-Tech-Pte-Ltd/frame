# Push notifications & widget refresh

## Problem
Apple (APNs) and Google (FCM) only accept pushes signed with the **app
publisher's** credentials. Self-hosters don't have (and must never get) those.
So Frame uses a tiny, **stateless, content-blind push relay** (`frame-relay`)
operated by the app publisher at `https://frame.crow.sg`. This is
the same pattern Signal, Matrix (Sygnal) and Mastodon use.

Self-hosters can disable push (`FRAME_PUSH_RELAY=`), in which case apps fall
back to periodic refresh (iOS widget timeline every ~15 min, Android WorkManager
every 15 min) plus live SSE while the app is open.

## Privacy properties
- The self-hosted server never sees raw APNs/FCM device tokens — only an
  opaque `relay_token` (device token sealed with the relay's AES-GCM key).
- The relay never sees content: every payload is AES-256-GCM encrypted with a
  random 32-byte `push_key` generated **on the device** and shared only with the
  user's own server (`PUT /api/v1/devices/current`).
- The relay is stateless (no database, no logs of payloads). It only knows
  "some server asked to push N bytes to some device".
- Visible fallback text shown if decryption fails is generic: title `Frame`,
  body `New activity`.

## Relay API (`frame-relay`)
Base `https://frame.crow.sg`. JSON. No auth (tokens are capabilities). Rate limited.

### `POST /v1/register` (called by the app)
```json
{"platform":"apns"|"apns-sandbox"|"fcm","token":"<device push token>",
 "app":"com.crowtech.frame","kind":"alert"|"widget"}
```
→ `{"relay_token":"<base64url( nonce12 || AES-256-GCM(relay_key, json{v:1,p,t,a,k,e}) )>"}`.
`kind:"widget"` is for iOS 26+ WidgetKit push tokens (APNs `apns-push-type: widgets`).
`app` must be in the relay's allowlist. Errors: `400` malformed, `403`
`app_not_allowed`, `429`.

**Relay tokens expire.** `e` is the expiry (unix seconds), set to issue time
**+ 60 days**, and it is mandatory. After `e`, `/v1/push` answers
`410 unregistered`, so the server deletes the device. Every registration returns
a new token with a fresh `e`, because the nonce is random. Apps therefore call
`/v1/register` on every launch and `PUT /devices/current` with the new
token(s). Devices in use never expire. A server the user has left (uninstalled
app, abandoned account) can push to that device for at most 60 days, and logout
or session revocation removes the device immediately.

### `POST /v1/push` (called by self-hosted servers)
```json
{"relay_token":"...","type":"alert"|"widget",
 "payload":"<base64 std: nonce12 || ciphertext || tag16>",
 "collapse_id":"moment-01J..."|null, "thread_id":"chat-01J..."|null}
```
- `payload` ≤ 3000 bytes (base64). Opaque to the relay.
- `type:"alert"` →
  - APNs: `apns-push-type: alert`, `apns-priority: 10`, topic = app bundle id,
    body `{"aps":{"alert":{"title":"Frame","body":"New activity"},"mutable-content":1,"sound":"default","thread-id":thread_id},"e":payload}`.
  - FCM HTTP v1: high-priority **data-only** message `{"data":{"e":payload}}`
    (Android app builds the notification itself).
- `type:"widget"` → APNs `apns-push-type: widgets`, topic
  `<bundle>.push-type.widgets`, body `{"aps":{"content-changed":true}}`
  (payload ignored). FCM: data message `{"data":{"w":"1"}}`.
- Responses:
  - `200 {"ok":true}`.
  - `410 {"error":"unregistered"}`: the server must delete that device. This
    covers tokens rejected by APNs/FCM, **expired** relay tokens, and relay
    tokens that no longer decrypt.
  - `400` malformed, `403 app_not_allowed`, `413`: permanent, don't retry.
  - `429` rate limited (60/min per device, 600/min per source IP), with
    `Retry-After`.
  - `502`/`503`/`504`: provider or relay trouble. Retry with backoff.

Relay secrets (relay host only): `RELAY_KEY` (32 bytes, base64), APNs `.p8`
key + key id + team id, FCM service account (or Cloud Run attached identity).
`RELAY_KEY` may be a comma-separated list: the first key seals new tokens and
all keys open old ones, so a key can be rotated without breaking devices. Drop
the old key once its tokens have expired (60 days). Removing a key outright
makes its tokens answer `410`, and apps recover on their next launch.

## Encrypted payload (plaintext JSON, before AES-GCM)
```json
{"v":1,"type":"moment"|"message"|"reaction"|"friend_request"|"friend_accepted",
 "title":"Alice","body":"sent a new photo",
 "moment_id":"01J..."|null,"peer_id":"01J..."|null,
 "thumb_path":"/m/01J...?e=..&s=.."|null,
 "created_at":1730000000000}
```
`peer_id` is **always** the other user involved, from the recipient's point of
view: the author (moment), the reactor (reaction), the sender (message), the
requester/accepter (friend_*). Clients use it for per-friend widget refresh,
avatars and notification threading.

Bodies (server-generated, English for v1; clients may localise by `type`):
- moment: `"<name>"` / `"sent a new photo"` / `"sent a new video"` (+ caption appended `: "…"`)
- message: `"<name>"` / message text (truncated 150 chars); reply-to-moment: `"replied to your photo: …"`
- reaction: `"<name>"` / `"reacted <emoji> to your photo"`
- friend_request: `"<name>"` / `"wants to be your friend"`
- friend_accepted: `"<name>"` / `"accepted your friend request"`

## Client behaviour
### iOS
- App registers for remote notifications → APNs token → relay `/v1/register`
  (`apns-sandbox` for debug builds, `apns` for TestFlight/App Store) → server.
- **Notification Service Extension**: decrypts `e` with `push_key` (shared via
  App Group keychain), rewrites title/body, downloads `thumb_path` (≤ 512 px)
  and attaches it; for `type=moment` it also writes the thumb + metadata into
  the App Group widget cache and calls `WidgetCenter.shared.reloadAllTimelines()`.
- iOS 26+: widget extension adopts WidgetKit push (`WidgetPushHandler`), its
  token is registered with the relay as `kind:"widget"` and sent to the server as
  `widget_relay_token`. The server sends a `widget` push on every new moment so
  widgets refresh even when notifications are muted/disabled.
- Widget timeline provider fetches `GET /moments/latest` itself (token in shared
  keychain) with `.after(15 min)` policy as fallback.

### Android
- Firebase Cloud Messaging (initialised manually from build config; if not
  configured the build still works and push is simply off).
- `FirebaseMessagingService.onMessageReceived` decrypts `e`, posts the
  notification (channels: Moments, Messages, Reactions, Friends), and for
  moments downloads the thumb and updates the Glance widget state immediately
  (expedited WorkManager job).
- Periodic WorkManager (15 min, network-connected) refreshes widgets as fallback.

### Server (when a push-worthy event happens)
For each recipient device with matching pref: build plaintext JSON, encrypt with
that device's `push_key`, `POST /v1/push`. For new moments also send `widget`
pushes to devices with `widget_relay_token`. Fire-and-forget worker queue with
retry (3 attempts, exponential backoff); delete device on 410.

## Sign in with Apple on Android (relay helper)
Apple's web flow needs a registered HTTPS return URL. The relay serves
`POST /apple/callback` (Apple `response_mode=form_post`): it renders a tiny page
that immediately redirects to
`frame://auth/apple#id_token=<..>&state=<..>&user=<json, first login only>`.
Nothing is stored. The app checks `state`, then sends `id_token` + `raw_nonce`
to its own server, which verifies it with `aud` = the Services ID.

## Invite landing
`https://frame.crow.sg/i#s=<server>&c=<code>` is a static page on the relay
domain. The server URL and code are in the URL **fragment**, so they never
reach the relay. The domain also serves
`/.well-known/apple-app-site-association` and `/.well-known/assetlinks.json`
so the link opens the installed app directly; otherwise the page shows store
links and a "copy server address" fallback.
