# Frame API contract — v1

This document is the **single source of truth** shared by the Frame server
(`server/`, Go) and the Frame iOS (SwiftUI) and Android (Kotlin/Compose) apps.
If an implementation needs to deviate, change this file first and note it in
the changelog at the bottom.

## Conventions

- Base URL: `<server>/api/v1`. `<server>` is whatever the user typed in the app
  (e.g. `https://frame.example.com`). Clients normalise it: trim, add `https://`
  if no scheme, strip trailing `/`. `http://` is accepted only for `localhost`,
  `10.0.2.2`, `127.0.0.1`, private LAN ranges and `.local` hosts (dev/LAN use).
- JSON in/out, UTF-8. Request bodies are limited (64 KiB for JSON) unless stated.
- Authentication: `Authorization: Bearer <token>` (opaque session token).
- IDs: 26-char Crockford base32 ULIDs (time-sortable), e.g. `01JC3V9Z4X8Q2M7T5R1N6K0B3H`.
- Timestamps: integer **milliseconds since Unix epoch** (UTC), fields end in `_at`.
- Optional fields are present with `null` (never omitted) in responses.
- Pagination: `?limit=N&before=<cursor>`; responses carry `next_cursor` (string
  or `null`). Cursors are opaque.
- Errors: HTTP status + body
  `{"error":{"code":"snake_case_code","message":"Human readable"}}`.
  Common codes: `bad_request` 400, `unauthorized` 401, `forbidden` 403,
  `not_found` 404, `conflict` 409, `payload_too_large` 413,
  `unsupported_media` 415, `rate_limited` 429 (with `Retry-After`),
  `registration_closed` 403, `invite_required` 403, `invalid_credentials` 401,
  `username_taken` 409, `identifier_taken` 409, `not_friends` 403,
  `blocked` 403, `internal` 500.
- Bearer-token failures are `401`. The code is `account_disabled` when the
  token belonged to a session that was revoked because the account was
  suspended (or the account is suspended), and `unauthorized` for every other
  missing/invalid/revoked token. Apps receiving `account_disabled` from any
  request — including the `GET /events` (re)connect — sign out locally and
  explain "This account has been suspended by the server admin." (v1.2.1).
- Idempotency: create endpoints accept a client-generated `client_id`
  (UUID string). Re-sending the same `client_id` from the same user returns the
  originally created object (200) instead of creating a duplicate.

## Objects

### UserSummary (public profile)
```json
{
  "id": "01J...",
  "username": "alice",            // string|null (null until onboarding sets it)
  "display_name": "Alice",
  "avatar": {"url": "/m/01J...?e=...&s=...", "width": 512, "height": 512} // MediaRef|null
}
```

### Me (private, extends UserSummary)
```json
{
  "...UserSummary fields": "...",
  "email": "alice@example.com",    // string|null
  "phone": "+34600111222",         // E.164 string|null
  "auth_methods": ["password", "apple", "google"],
  "discoverable": true,            // may be found by contact matching
  "searchable": true,              // may be found by username search
  "read_receipts": true,           // send read receipts (if false, peers never see read_at for my reads)
  "streak": {"count": 4, "last_date": "2026-11-07"}, // daily posting streak (see Moments: local_date)
  "stats": {"moments": 128, "friends": 9},       // own moment count (all audiences) and friend count
  "is_admin": false,                             // may open the web admin panel (/admin)
  "created_at": 1730000000000
}
```
Streak: number of consecutive `local_date`s (client-local calendar days) with ≥ 1
own moment, ending at the most recent posting day. Clients show it only if
`last_date` is today or yesterday in the device's local calendar (else 0).

### MediaRef
```json
{"url": "/m/01JMEDIA?e=1730086400&s=BASE64URL", "width": 1080, "height": 1080,
 "content_type": "image/jpeg", "duration_ms": null, "bytes": 312345}
```
- `url` is **relative to `<server>`**, signed, and valid for 24 h. It needs **no
  Authorization header** (so AVPlayer / ExoPlayer / widgets can load it). It
  supports single-range HTTP `Range` requests (video seeking); a multi-range
  request gets the whole file (`200`).
- Clients cache media **by media id** (the path segment after `/m/`), never by
  full URL (signatures change). Media bytes for an id never change.

### Caption
```json
{"type": "text", "text": "sunset 🌅", "style": "classic", "color": null, "background": null, "data": null}
```
- `type`: `text` (default) | `location` | `time` | `streak` in v1 clients;
  reserved for later: `weather`, `music`, `review`, `link`. **Every caption has a
  human-readable `text`**; clients render unknown types as plain `text` with the
  given style. `data` is a small free-form JSON object (≤ 1 KiB) the server stores
  verbatim, e.g. location `{"name":"Café Central"}` (never coordinates in v1),
  review `{"rating":4}`. Icons per type are client-side (📍 location, 🕒 time, 🔥 streak).
- `text`: 1–`limits.max_caption_length` (default 64) Unicode scalars, single line.
- `style`: one of `classic` (translucent dark pill, white text — default),
  `bold` (solid white pill, black text), `glass` (frosted pill), `color`
  (solid pill using `background` + `color`, hex `#RRGGBB`), `script` (handwritten
  look, no pill). Unknown styles must render as `classic` (forward compatible).
- Captions are rendered by clients over the bottom-centre of the square image;
  never burned into the image bytes.

### Moment
```json
{
  "id": "01J...",
  "author": {UserSummary},
  "kind": "photo",                 // "photo" | "video"
  "created_at": 1730000000000,
  "caption": {Caption} | null,
  "media": {MediaRef},             // full photo (≤2048 px, usually 1440×1440 JPEG) or MP4 video
  "thumb": {MediaRef},             // ≤ 512×512 JPEG (photo downscale or video poster frame)
  "audience": "all",               // "all" | "selected" | "self" ("Just me"); meaningful to the author
  "recipient_ids": ["01J..."],     // author only; null for others
  "local_date": "2026-11-07",      // author's local calendar day at capture (drives streaks/calendar)
  "reactions": [{"user": {UserSummary}, "emoji": "❤️", "created_at": 1730000000000}],
                                   // author sees all; others see only their own
  "viewers": [{"user": {UserSummary}, "viewed_at": 1730000000000}],
                                   // author only; null for others
  "viewed": true                   // whether the requesting user has viewed it
}
```

### Message
```json
{
  "id": "01J...",
  "client_id": "uuid",
  "sender_id": "01J...",
  "recipient_id": "01J...",
  "text": "love this",
  "created_at": 1730000000000,
  "moment": {MomentRef} | null,    // when this message is a reply to a moment
  "reactions": [{"user_id": "01J...", "emoji": "😍"}], // ≤ 1 per participant
  "read_at": null                  // when recipient read it (null if recipient disabled read receipts)
}
```
MomentRef: `{"id","author_id","kind","thumb": MediaRef|null,"caption": Caption|null,"deleted": false}`
(`thumb` is null and `deleted` true when the moment no longer exists or is no
longer visible to the reader).

### Conversation
```json
{"peer": {UserSummary}, "last_message": {Message}|null, "unread_count": 2, "updated_at": 1730000000000}
```

### FriendRequest
```json
{"id": "01J...", "from": {UserSummary}, "to": {UserSummary}, "created_at": 1730000000000}
```

## Endpoints

### Server discovery (public)
`GET /api/v1/server` →
```json
{
  "name": "Daniel's Frame",
  "version": "1.0.0",
  "api_version": 1,
  "server_id": "01J...",
  "registration": "invite",        // "open" | "invite" (default) | "closed"
  "user_invites": true,            // on "invite" servers: can members' friend-invite links register new people?
  "auth_methods": {"email": true, "phone": true, "apple": true, "google": true},
  "password_reset": "email",       // "email" (SMTP configured) | "admin"
  "limits": {"max_video_ms": 15000, "max_upload_bytes": 52428800,
             "max_caption_length": 64, "max_friends": null},
  "push": {"relay_url": "https://frame.crow.sg"} // or null when push disabled
}
```
`GET /healthz` → `200 ok` (plain text).

### Auth
All return `{"token": "...", "user": {Me}, "is_new": bool}` unless stated.
`device` object everywhere: `{"name": "Alice's iPhone", "platform": "ios"|"android"}`.

- **Registration gate.** Servers default to `registration:"invite"`: creating a
  new account (password, Apple or Google) requires a valid `invite_code` — either
  an admin-created *server invite* (limited uses/expiry) or, when `user_invites`
  is true, any member's friend-invite code (the new user is then instantly
  friends with the inviter). Errors: `invite_required` (none given),
  `invite_invalid` (unknown/expired/revoked/used up — one generic code).
  Signing in to an existing account never needs a code. `closed` = only admins
  create accounts. Suspended accounts get `account_disabled` (403) on every
  login and their sessions are revoked; requests with those revoked bearer
  tokens get `401 account_disabled` until the account is reinstated (then
  plain `401 unauthorized`).
- `POST /api/v1/auth/register`
  `{"method":"email"|"phone","identifier":"...","password":"...","display_name":"Alice","invite_code":null,"device":{...}}`
  Password: 8–128 chars. Email lowercased/trimmed; phone must be E.164
  (`^\+[1-9]\d{6,14}$`). Errors: `identifier_taken`, `registration_closed`,
  `invite_required`, `bad_request`.
- `POST /api/v1/auth/login` `{"method","identifier","password","device"}` →
  `invalid_credentials` on any failure (no user enumeration).
- **Server-bound nonce (required for Apple and Google).** Every Frame server
  accepts the same app audiences, so an ID token must be bound to the server it
  was obtained for — otherwise a malicious server could replay a user's token
  to that user's real server. The client builds
  `raw_nonce = "frame1:" + <server_host> + ":" + <32 random bytes, base64url, no padding>`
  where `<server_host>` is the lowercase host of the server base URL the client
  is using, with `:port` only when non-default for the scheme (e.g.
  `frame.example.com`, `192.168.1.5:8080`). The provider receives
  `nonce = sha256hex(raw_nonce)` (Apple `nonce` param / Google `nonce` param).
  The server requires `raw_nonce`, parses `<server_host>` (between the `frame1:`
  prefix and the **last** `:`), and accepts it only if it equals one of the
  server's **configured** public hosts (from `FRAME_PUBLIC_URL`, which may be a
  comma-separated list, and `FRAME_DOMAIN`) — never the request `Host` header,
  which an attacker controls. Each `raw_nonce` is single-use (server remembers
  `sha256(raw_nonce)` until the token's `exp`). If no public host is configured,
  the server reports `apple:false, google:false` in `/server` (dev mode accepts
  any host). Error: `invalid_credentials` (no detail).
- `POST /api/v1/auth/apple` `{"id_token":"...","raw_nonce":"...","display_name":null,"invite_code":null,"device":{...}}`
  Server verifies RS256 signature (Apple JWKS), `iss=https://appleid.apple.com`,
  `aud` ∈ configured Apple audiences (iOS bundle id, Android Services ID),
  `exp`, the server-bound nonce above and `nonce == sha256hex(raw_nonce)`.
  Account keyed by `sub`. If the token carries a verified email that matches an
  existing *email* account it is **not** auto-linked (explicit linking only).
  When called with a valid Bearer session, links this identity to the current
  account instead of signing in.
- `POST /api/v1/auth/google` `{"id_token":"...","raw_nonce":"...","invite_code":null,"device":{...}}`
  Verifies RS256 (Google JWKS), `iss ∈ {accounts.google.com, https://accounts.google.com}`,
  `aud` ∈ configured Google client IDs, `exp`, server-bound nonce (required).
  Keyed by `sub`. Linking as for Apple.
- `POST /api/v1/auth/logout` → 204 (revokes this session + its push device).
- `GET /api/v1/auth/sessions` → `{"sessions":[{"id","device_name","platform","created_at","last_seen_at","current":bool}]}`
- `DELETE /api/v1/auth/sessions/{id}` → 204.
- `POST /api/v1/auth/password` `{"current_password": "..."|null, "new_password": "..."}` → 204.
  Revokes all **other** sessions (and their push devices, closing their SSE streams).
  `current_password` may be null only if the account has no password yet (adds
  password login to an Apple/Google account; requires `identifier` field
  `{"method":"email"|"phone","identifier":"..."}` in that case).
- `POST /api/v1/auth/password-reset/request` `{"email":"..."}` → 204 always
  (only when SMTP configured; else 404).
- `POST /api/v1/auth/password-reset/confirm` `{"email","code","new_password"}` → 204.

### Me
- `GET /api/v1/me` → Me
- `PATCH /api/v1/me` `{"display_name"?: "...", "username"?: "...", "discoverable"?: bool, "searchable"?: bool, "read_receipts"?: bool}` → Me.
  Username: 3–20 chars of `a-z 0-9 . _`, stored lowercase, unique; must start
  with a letter, end with a letter or digit, and not contain `..`, `__`, `._`
  or `_.`. Client-side messages: "Must be at least 3 characters", "Can't be
  longer than 20 characters", "Only letters, numbers, _ and . allowed", "Must
  start with a letter", "Must end with a letter or number", "Can't contain
  consecutive . or _", server `username_taken`. Usernames can be changed.
  display_name 1–40 chars.
- `PUT /api/v1/me/avatar` raw body `image/jpeg` (≤ 2 MiB, ≤ 1024×1024, client
  pre-crops square) → Me. `DELETE /api/v1/me/avatar` → Me.
- `DELETE /api/v1/me` `{"confirm": "DELETE"}` → 204. Deletes the account and
  **all** its moments, media, messages, friendships, sessions. Irreversible.

### Users & discovery
- `GET /api/v1/users/{id}` → `{"user": UserSummary, "friendship": "self"|"friends"|"incoming"|"outgoing"|"none"|"blocked"}`
- `GET /api/v1/users/search?q=ali` → `{"users":[{"user":UserSummary,"friendship":"..."}]}`
  Case-insensitive username prefix match, `q` ≥ 2 chars, max 20 results,
  excludes blocked/blocking users and users with `searchable=false` (an exact
  username match of a non-searchable user is also excluded).
- `POST /api/v1/contacts/match` `{"hashes":["<64 hex>", ...]}` (≤ 2000) →
  `{"matches":[{"hash":"...","user":UserSummary,"friendship":"..."}]}`
  Hash = lowercase hex SHA-256 of `"frame-contact-v1:" + normalized`, where
  normalized is an E.164 phone (`+34600111222`) or a lowercased trimmed email.
  Only users with `discoverable=true` match. Rate limited (10 calls/hour/user).
  The server never stores the uploaded hashes.

### Friends
- `GET /api/v1/friends` → `{"friends":[{"user":UserSummary,"since":1730000000000}]}`
- `GET /api/v1/friends/requests` → `{"incoming":[FriendRequest],"outgoing":[FriendRequest]}`
- `POST /api/v1/friends/requests` `{"user_id":"..."}` →
  `{"status":"requested","request":FriendRequest}` or, if the other user had
  already requested you, `{"status":"friends","request":null}` (auto-accept).
  Errors: `blocked`, `conflict` (already friends). No friend limit unless the
  server sets `max_friends`.
- `POST /api/v1/friends/requests/{id}/accept` → `{"friend":{"user":...,"since":...}}`
- `POST /api/v1/friends/requests/{id}/decline` → 204
- `DELETE /api/v1/friends/requests/{id}` → 204 (cancel outgoing)
- `DELETE /api/v1/friends/{user_id}` → 204 (unfriend; both lose access to each
  other's moments and the chat becomes read-only)
- `GET /api/v1/blocks` → `{"users":[UserSummary]}`
- `POST /api/v1/blocks` `{"user_id":"..."}` → 204 (also unfriends, removes requests)
- `DELETE /api/v1/blocks/{user_id}` → 204

### Invites
An invite lets someone become your friend instantly (and register on
invite-only servers).
- `POST /api/v1/invites` → `{"code":"<22 char base64url>","url":"https://frame.crow.sg/i#s=<urlencoded server>&c=<code>","expires_at":...}`
  (valid 30 days, unlimited uses; `url` host comes from server config
  `FRAME_INVITE_BASE`, default `https://frame.crow.sg/i`).
- `GET /api/v1/invites/{code}` (public, rate-limited) →
  `{"kind":"friend"|"server","inviter": UserSummary|null,"server_name":"...","can_register":true}`
  (`server` invites are created by an admin in the admin panel: they let someone
  register but don't create a friendship; `can_register` is false when the code
  can only befriend existing users, i.e. a member's link while `user_invites` is off).
- `POST /api/v1/invites/{code}/redeem` →
  `{"status":"friends","friend":{"user":UserSummary,"since":...},"request":null}`
  (no-op if already friends), **or** `{"status":"requested","friend":null,"request":FriendRequest}`
  when the redeemer is someone the inviter previously **unfriended or blocked**
  (then it only creates a normal friend request the inviter must accept; a
  blocked redeemer gets `blocked`). Registration with `invite_code` redeems automatically.
  A `server` invite only lets new people register: redeeming one while signed in
  returns `400 bad_request`.
- `GET /api/v1/invites` → `{"invites":[{"code","url","created_at","expires_at","uses"}]}` (mine, unexpired).
- `DELETE /api/v1/invites/{code}` → 204 (revoke; mine only). Settings shows
  "Reset invite link" = revoke all + create new.

App deep links (both platforms handle all three):
- `https://frame.crow.sg/i#s=<server>&c=<code>` (universal link / app link)
- `frame://invite?s=<server>&c=<code>`
- `frame://moment?id=<moment_id>` / `frame://chat?peer=<user_id>` (from widget & notifications)

### Moments
- `POST /api/v1/moments` — `multipart/form-data`, ≤ `max_upload_bytes`:
  - part `meta` (application/json):
    `{"client_id":"uuid","kind":"photo"|"video","caption":Caption|null,
      "audience":"all"|"selected"|"self","recipient_ids":["..."],
      "width":1440,"height":1440,"duration_ms":null,"local_date":"2026-11-07"}`
    (`recipient_ids` required non-empty for `selected`, ignored otherwise;
    `self` = visible only to the author — "Just me")
  - part `media`: `image/jpeg` | `image/heic` (photo) or `video/mp4` (video,
    H.264 or HEVC, AAC audio, ≤ `max_video_ms`)
  - part `thumb`: `image/jpeg`, ≤ 512×512, ≤ 200 KiB
  → `201 Moment`. The server validates magic bytes (JPEG `FF D8 FF`, HEIC
  `ftypheic/heix/mif1`, MP4 `ftyp`), sizes and that recipients are friends. It
  never decodes images. Clients MUST strip metadata (EXIF/GPS) — re-encoding
  from pixels does this — and MUST produce square media (crop/letterbox client-side).
- `GET /api/v1/moments?limit=30&before=<cursor>&user_id=<id>&from=<ms>&to=<ms>` →
  `{"moments":[Moment],"next_cursor":...}` — own moments + friends' moments
  you are a recipient of (minus ones you hid), newest first. `user_id` filters
  to one author (yourself or a friend); `from`/`to` bound `created_at`
  (inclusive/exclusive) — used by the profile calendar.
- `GET /api/v1/moments/latest?user_id=<friend id>` →
  `{"moment": Moment|null, "unseen_count": 3}` — newest moment from friends
  (excluding your own) or from the given friend, plus how many visible friend
  moments in that scope you haven't viewed (widget badge). Used by widgets.
  Responds with a weak `ETag` derived from (moment id, unseen_count, caption
  hash); clients send `If-None-Match` and get `304 Not Modified` when nothing
  changed (cheap background widget refreshes).
- `POST /api/v1/moments/{id}/hide` → 204 — "Delete for me" on someone else's
  moment (removes it from your feed/widget only).
- `GET /api/v1/moments/{id}` → Moment (404 if not visible).
- `DELETE /api/v1/moments/{id}` → 204 (author only; deletes media too).
- `POST /api/v1/moments/views` `{"ids":["..."]}` → 204 (mark viewed; ignores
  own/invisible ids; max 100).
- `PUT /api/v1/moments/{id}/reaction` `{"emoji":"❤️"}` → 204. One reaction per
  user per moment (replaces previous). Emoji: 1–16 bytes UTF-8, must be a single
  grapheme. Not allowed on own moments.
- `DELETE /api/v1/moments/{id}/reaction` → 204.
- Replying to a moment = sending a message with `reply_to_moment_id` (below).

### Chat (1:1, friends only)
- `GET /api/v1/conversations` → `{"conversations":[Conversation]}` (newest first;
  includes ex-friends with history).
- `GET /api/v1/conversations/{peer_id}/messages?limit=50&before=<cursor>` →
  `{"messages":[Message],"next_cursor":...}` (newest first).
- `POST /api/v1/conversations/{peer_id}/messages`
  `{"client_id":"uuid","text":"...","reply_to_moment_id":null|"..."}` → `201 Message`.
  Text 1–2000 chars. Requires friendship (`not_friends`). `reply_to_moment_id`
  must be a moment visible to both.
- `POST /api/v1/conversations/{peer_id}/read` `{"up_to":"<message id>"}` → 204.
- `POST /api/v1/conversations/{peer_id}/typing` → 204 (fan-out event only, not stored).
- `DELETE /api/v1/messages/{id}` → 204 (sender only; "unsend").
- `PUT /api/v1/messages/{id}/reaction` `{"emoji":"😍"}` → 204 (either participant,
  one per user, replaces); `DELETE /api/v1/messages/{id}/reaction` → 204.
  Default quick reactions in clients: 💛 😍 🤣 😭 🤯 (moments: 💛 🔥 😂 😍).

### Reports
Members can report a moment, a message or a user to **their server's admin**
(whoever runs the server). Nothing is scanned automatically: the admin reviews
reports in the web panel ([ADMIN.md](ADMIN.md#reports-and-the-content-exception)).
- `POST /api/v1/reports`
  `{"target":{"type":"moment"|"message"|"user","id":"01J..."},"reason":"spam"|"harassment"|"nudity"|"violence"|"self_harm"|"other","note":"..."|null,"block":false}`
  → 204.
  - The reporter must be able to see the target: a moment they can open
    (`GET /moments/{id}`: they are a recipient and still friends with the
    author), a message in their conversation **sent by the other participant**,
    or a user they can look up (`GET /users/{id}`). Anything else is
    `404 not_found` with the same body as an id that doesn't exist. Your own
    moment or message, or yourself, is `400 bad_request`.
  - `reason` must be one of the six values; `note` is optional, trimmed, at
    most 500 characters (newlines allowed, other control characters not);
    blank means `null`. Otherwise `400 bad_request`.
  - Reporting a **moment** also hides it from the reporter's feed and widget,
    like `POST /moments/{id}/hide`.
  - `block: true` also blocks the author (moment author, message sender or the
    user) exactly like `POST /blocks`: unfriends, drops pending requests, and
    both sides get `friend.removed` when they were friends.
  - Reporting the same target again while your earlier report is still open
    returns 204 but stores no second report (hide/block still apply).
  - Rate limited: 30 reports per hour per user.
  - Clients show Report only on other people's content and profiles, and
    confirm with something like "Thanks. The server's admin will review it."

### Realtime events
`GET /api/v1/events` with Bearer auth → `text/event-stream` (Server-Sent
Events). Server sends `: ping` every 25 s. Each event:
```
event: message.new
data: {"message": Message}

```
Event types and `data`:
| event | data |
|---|---|
| `moment.new` | `{"moment": Moment}` |
| `moment.deleted` | `{"id": "..."}` |
| `moment.reaction` | `{"moment_id","user":UserSummary,"emoji":"❤️"\|null}` (to author) |
| `moment.viewed` | `{"moment_id","user":UserSummary,"viewed_at"}` (to author) |
| `message.new` | `{"message": Message}` |
| `message.deleted` | `{"id","peer_id"}` |
| `message.read` | `{"peer_id","up_to","read_at"}` (not sent if the reader disabled read receipts) |
| `message.reaction` | `{"message_id","peer_id","user_id","emoji":"😍"\|null}` |
| `typing` | `{"peer_id"}` |
| `friend.request` | `{"request": FriendRequest}` |
| `friend.accepted` | `{"friend":{"user":UserSummary,"since":...}}` |
| `friend.removed` | `{"user_id"}` |
| `profile.updated` | `{"user": UserSummary}` (friends' profile changes) |
Clients reconnect with backoff and re-fetch state after reconnecting (no replay).

### Push devices
- `PUT /api/v1/devices/current`
  ```json
  {"platform":"ios"|"android",
   "relay_token":"<opaque string from relay>",
   "widget_relay_token":"<opaque>"|null,   // iOS 26+ WidgetKit push token via relay
   "push_key":"<base64 32 random bytes>",  // AES-256-GCM key generated on device
   "prefs":{"moments":true,"messages":true,"reactions":true,"friends":true}}
  ```
  → 204. One device per session; replaced on each call. Removed on logout/session revoke.
- `DELETE /api/v1/devices/current` → 204.

Push flow, payloads and the relay protocol are in [PUSH.md](PUSH.md).

### Media
- `GET /m/{media_id}?e=<unix seconds>&s=<base64url HMAC-SHA256>` — serves bytes
  with exact `Content-Type`, `Cache-Control: private, max-age=86400, immutable`,
  `X-Content-Type-Options: nosniff`, `Accept-Ranges: bytes`.
  Signature = HMAC-SHA256(server media key, `"<media_id>.<e>"`). 403 if expired/invalid.

## Rate limits (defaults)
- Auth endpoints: 10/min per IP, 5/min per identifier and IP, 30/min per
  identifier overall; 429 + `Retry-After`.
- Writes (moments): 60/hour per user; messages 60/min per user.
- Reports: 30/hour per user.
- Everything else: 600/min per user.

## Changelog
- v1.0 (2026-11-07): initial contract.
- v1.0.1 (2026-11-07, before any client shipped): from feature research —
  audience `self` ("Just me"); `local_date` on moments + `streak` on Me;
  `searchable` & `read_receipts` on Me; Caption `type`/`data`; username rules
  3–20 chars (start letter, end alnum, no consecutive separators);
  `unseen_count` on `/moments/latest`; `POST /moments/{id}/hide`; `from`/`to`
  on `GET /moments`; message reactions (+ `message.reaction` event; Message
  `reaction_emoji` replaced by `reactions`).
- v1.0.2: weak `ETag` / `If-None-Match` → `304` on `GET /moments/latest`.
- v1.0.3 (server clarifications, backward compatible — no shape changes):
  Google `nonce`, when `raw_nonce` is non-null, must equal `sha256hex(raw_nonce)`
  (same rule as Apple). Accounts created via Apple/Google start with
  `email: null` (token emails are never stored or linked). Calling
  `/auth/apple` or `/auth/google` *with* a valid `Authorization` header links
  that identity to the signed-in account (explicit linking) and returns the
  current token (`is_new:false`; `409 identifier_taken` if linked elsewhere).
  Auth endpoints return `200`. Extra error codes: `friend_limit` 403 (only when
  `max_friends` is set), `method_not_allowed` 405, `unavailable` 503 (Apple/Google
  keys unreachable). `moment.new`, `message.new`, `friend.request` and
  `message.read` are also delivered to the actor's own other devices
  (`peer_id` is always "the other participant" from the receiver's view).
  The `/moments/latest` ETag also rotates hourly with the signed-URL expiry so
  a `304` never pins expired media URLs.
- v1.1 (2026-11-07, security review): **server-bound `raw_nonce` required** for
  Apple and Google (`frame1:<server_host>:<random>`, checked against configured
  public hosts, single-use) — fixes cross-server ID-token replay; password change
  revokes other sessions; invite redemption by a previously unfriended/blocked
  user only creates a friend request (`status` field in redeem response);
  `GET /invites`, `DELETE /invites/{code}`; revoked sessions' SSE streams close
  (including revocations by the admin CLI, re-validated at every ping).
- v1.2 (2026-11-07, owner request): registration defaults to **invite-only**;
  `/server.user_invites`; admin-created *server invites*; `GET /invites/{code}`
  gains `kind` + `can_register` (inviter nullable); errors `invite_invalid`,
  `account_disabled`; Me gains `stats` and `is_admin`. Web admin panel + its
  API are specified in [ADMIN.md](ADMIN.md) (not used by the mobile apps).
  Clarifications from the server implementation: on `open` servers an unusable
  `invite_code` is ignored (no error); invites of suspended members don't
  resolve (`404` / `invite_invalid`); `account_disabled` is returned only after a
  correct password (a wrong one is still `invalid_credentials`); a suspended
  account's bearer tokens get `401`; redeeming a `server` invite is `400`.
- v1.2.1 (2026-11-08, owner request): bearer tokens revoked by a **suspension**
  get `401` with code **`account_disabled`** (instead of `unauthorized`) on every
  endpoint, including `GET /events`, as long as the account stays suspended. The
  server remembers only SHA-256 of those tokens (table `revoked_sessions`),
  drops them when the account is reinstated or deleted, and never reports
  `account_disabled` for unknown, logged-out or otherwise revoked tokens (those
  keep `401 unauthorized`). Apps sign out and show "This account has been
  suspended by the server admin." on the sign-in screen. Backwards compatible:
  older apps treat any `401` as "session ended". Fixture:
  `fixtures/error_account_disabled.json`.
- v1.3 (2026-11-09, owner request; App Store guideline 1.2): **reports**.
  `POST /reports` lets a member report a moment, message or user they can see
  to the server's admin, optionally blocking the author in the same call;
  reporting a moment hides it for the reporter. Reports are reviewed by a
  person in the admin panel (delete the content, suspend the author, or
  dismiss); there is still no automatic scanning. New rate limit: 30
  reports/hour/user. Backwards compatible: one new endpoint, no changed shapes;
  older apps simply don't offer Report.
- v1.3.1 (2026-11-09, security review; backwards compatible, no shape
  changes): `GET /m/{id}` honours a single `Range` only. A multi-range
  request (`Range: bytes=0-0,9-9,…`) gets the whole file with `200`, so one
  signed URL can't make the server issue up to 200 storage reads (ranged
  GETs against S3/R2) per request. AVPlayer, ExoPlayer and image loaders
  only send single ranges. The auth rate limits above are stated precisely
  (unchanged behaviour).
