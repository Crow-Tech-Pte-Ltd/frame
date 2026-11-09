# frame-server

The self-hosted Frame server. It's one static Go binary with SQLite (pure Go,
no CGO) and a single data directory. It implements the contract in
[`docs/API.md`](../docs/API.md). Configuration is documented in
[`docs/CONFIG.md`](../docs/CONFIG.md), and push in [`docs/PUSH.md`](../docs/PUSH.md).

Dependencies: Go stdlib, `modernc.org/sqlite`, and `golang.org/x/crypto`
(argon2id, acme/autocert; this pulls in `golang.org/x/net/idna` and
`golang.org/x/text` transitively). There is no web framework, ORM or JWT
library. `go.mod` pins `toolchain go1.26.10`, which has every current stdlib
security fix; with the default `GOTOOLCHAIN=auto`, older Go installs download
it automatically.

## Dev quickstart

```sh
cd server
go run ./cmd/frame-server dev
```

- The dev server listens on `0.0.0.0:8787` and stores data in `server/.devdata/`
  (or `./.devdata` when run outside the repo).
- Override either with env vars:
  `FRAME_LISTEN=0.0.0.0:8788 FRAME_DATA_DIR=/tmp/frame-b go run ./cmd/frame-server dev`.
- Registration is open (production defaults to invite-only), local storage is
  used, and push is off unless `FRAME_PUSH_RELAY` is set. Rate limits are
  relaxed ×100 for UI tests.
- `alice` is an admin of the web panel at `http://localhost:8787/admin/`, so
  dev mode prints no first-run setup link.
- On first run it seeds the following data. Seeding is idempotent; delete
  `.devdata` to start over.
  - Three users who are all mutual friends, with password `password123`:
    `alice`, `bob` and `carol`. Their emails are `alice@example.com` etc. and
    their phones `+15550000001`–`3`.
  - Generated avatars and 12 moments spread over the last two days. They
    include captions in several styles, a location caption, a "selected"
    audience moment and a short MP4 video.
  - Views and reactions, plus a chat with two moment replies, a message
    reaction and a read receipt.
- Once listening, it prints the demo credentials and then exactly one line,
  `FRAME DEV READY http://<listen>`, which scripts can wait on.
- Android emulator: `http://10.0.2.2:8787`. iOS simulator: `http://localhost:8787`.

Useful commands:

```sh
go test ./...                                   # unit + HTTP e2e + authz matrix
go test -race ./...
go test ./... -run TestWriteFixtures -update    # regenerate docs/fixtures/*.json + events.txt
go test ./internal/api -run '^$' -bench .       # feed benchmarks (200 friends, 50k moments)
go test ./internal/api -run '^$' -fuzz FuzzMomentUpload -fuzztime 30s
```

`TestWriteFixtures` records real handler responses into `docs/fixtures/` for
the mobile apps' decoding tests. Without `-update` it fails when a response
shape drifts from the committed fixtures.

The web admin panel is built from [`admin-ui/`](../admin-ui/README.md) into
`internal/adminui/dist/`, which is committed and embedded with `go:embed`, so
`go build` never needs Node or Bun. After changing the UI:

```sh
cd ../admin-ui && bun install --frozen-lockfile && bun run build
```

Release build (static, ~14 MB):

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o bin/frame-server-linux-arm64 ./cmd/frame-server
```

## CLI

As in CONFIG.md:

- `serve` (the default)
- `dev`
- `admin setup-link` (one-time link to create the first admin)
- `admin user list|create [--admin]|promote|demote|reset-2fa|reset-password|delete`
- `admin invite create <username>` (friend invite) and
  `admin invite server [--uses N] [--days D] [--note T]` (server invite)
- `backup <file.db>`, an online `VACUUM INTO` copy; also back up `secrets.json` and `media/`
- `version`

Admin commands work directly on the data directory and are safe while the
server is running. They are recorded in the audit log as `CLI`.

## Architecture

```
cmd/frame-server   CLI, bootstrap (data dir, secrets.json, DB, storage), HTTP/TLS servers, graceful shutdown
internal/config    env + KEY=value file parsing/validation (CONFIG.md)
internal/db        SQLite open (WAL, busy_timeout, foreign keys), 1 writer conn + read-only pool, embedded migrations
internal/store     every SQL query (parameterised), typed rows
internal/api       HTTP handlers, middleware, JSON views, validation, SSE, signed media
internal/auth      Argon2id PHC hashing, session tokens, identifier/username rules
internal/oidc      Apple/Google ID-token verification (RS256 only, JWKS cache)
internal/media     magic-byte sniffing, JPEG header dimensions, URL signing, blob service + orphan sweep
internal/storage   Storage interface: local (sharded, atomic) and S3 (hand-written SigV4) + encryption wrapper
internal/push      payload encryption + bounded fan-out/send queues to the relay
internal/events    in-memory per-user SSE hub
internal/ratelimit token buckets with bounded memory
internal/admin     admin CLI
internal/adminui   embedded web admin panel (dist/ built from admin-ui/) + SPA file server
internal/totp      RFC 6238 TOTP and recovery codes for admin two-factor auth (stdlib only)
internal/devseed   dev demo data (drives the real handlers in-process)
```

### Requests

- `outer` middleware runs on every request:
  - resolves the client IP (`FRAME_TRUSTED_PROXIES`)
  - applies a 64 KiB `MaxBytesReader`; uploads set their own limits
  - sets security headers (nosniff, `X-Frame-Options: DENY`, a CSP sandbox,
    `Referrer-Policy: no-referrer`, HSTS on TLS, `Cache-Control: no-store`
    on `/api`)
  - recovers panics as `500 {"error":{"code":"internal"}}`
  - writes JSON access logs.
- Access logs record the matched route pattern, never the raw path or query,
  so media signatures and invite codes stay out. They never contain bodies,
  tokens, passwords, captions or message text; `TestLogsNeverContainSecrets`
  checks this.
- There is no CORS. The only static files are the embedded admin panel under
  `/admin/` (its own strict CSP; see below).
- Unknown routes and methods return JSON 404/405.
- Authenticated handlers look up `SHA-256(bearer)` in `sessions`.
  `last_seen_at` is updated at most every 5 minutes.
- Server timeouts are `ReadHeaderTimeout` 10 s, Read/Write 60 s and Idle 120 s.
  The upload handler extends its read deadline and media downloads extend
  their write deadline. SSE lifts both via `http.ResponseController` and sets
  a 30 s deadline per write.
- Rate limits (token buckets keyed by IP, IPv6 /64, user or identifier):
  - auth: 10/min per IP, 5/min per (identifier, IP) and 30/min per
    identifier globally, so an attacker can't lock a victim out
  - password-reset confirms: 10 per user per 24 h, however many codes were
    requested
  - moments: 60/h; messages: 60/min
  - contacts: 10/h; invite lookups: 30/min per IP
  - everything else: 600/min per user
  - exhausted buckets return `429` with `Retry-After`.
- Trusted proxies: `X-Forwarded-For` is honoured only when the direct peer is
  in a configured CIDR. The server walks it right to left past trusted hops.
  With `cloudflare`, `CF-Connecting-IP` is honoured only from loopback or an
  explicitly listed CIDR (e.g. `cloudflare,172.18.0.0/16` for a cloudflared
  container), never from arbitrary private peers.

### Registration gate (API.md v1.2)

- `registration` (`invite` by default, `open`, `closed`), `user_invites`,
  the server name and the limits are **live settings**: the configuration is
  the base, values saved in the admin panel (`settings` table) are layered on
  top unless the key was set in the environment/config file
  (`Config.Explicit`), in which case it is locked. Handlers read them through
  `s.live()`; `ReloadSettings` recomputes them.
- New accounts (password, Apple, Google) go through `checkRegistration`:
  - `closed` → `registration_closed`; `invite` without a code →
    `invite_required`; any unusable code → `invite_invalid` (one generic
    code); `open` ignores unusable codes.
  - A code is looked up first among admin **server invites**
    (`server_invites`: max uses, optional expiry, revocable, note; no
    friendship) and then among members' **friend invites** (only on `open`
    servers or when `user_invites` is on; instant friendship as before).
    Invites of suspended members never resolve.
  - The server-invite use is consumed in the same transaction as the user
    insert with a conditional `UPDATE … WHERE uses < max_uses`, so concurrent
    sign-ups can't overshoot and a failed insert doesn't burn a use.
  - `users.invited_by` / `users.server_invite_id` record how someone joined.
- `GET /invites/{code}` returns `kind`, nullable `inviter` and
  `can_register`; redeeming a server invite while signed in is `400`.
- **Suspension** (`users.disabled_at`): deletes all app and admin sessions
  (push devices cascade, SSE streams close), login returns `account_disabled`
  only after a correct password, bearer auth re-checks the flag, and the user
  disappears from search and contact matching. Content stays.
- Me gains `stats` (two indexed `COUNT`s) and `is_admin`.

### Web admin panel (docs/ADMIN.md)

- Routes under `/admin/api/v1/` live in a separate mux (`admin*.go`) and are
  served on the main listener, only to loopback/trusted networks
  (`FRAME_ADMIN=local`), only on `FRAME_ADMIN_LISTEN`, or not at all (`off`).
  Every `/admin` response carries `Content-Security-Policy: default-src
  'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self';
  frame-ancestors 'none'; base-uri 'none'; form-action 'self'`.
- Sessions: cookie `frame_admin` (256-bit, SHA-256 stored in
  `admin_sessions`), `HttpOnly`, `SameSite=Strict`, `Path=/admin`, `Secure`
  over TLS or behind a trusted https proxy; 30 min idle / 12 h absolute. The
  admin flag and suspension are re-checked on every request (`403` once an
  account is no longer an admin).
- CSRF: non-GET requests need `X-Frame-Admin: 1` and a same-origin `Origin`
  (request origin or a configured `FRAME_PUBLIC_URL` origin) or
  `Sec-Fetch-Site: same-origin`.
- Login: e-mail, phone or username + password (+ TOTP or recovery code);
  the app's per-IP / per-identifier rate limits; non-admins get
  `invalid_credentials`.
- TOTP secrets are AES-GCM sealed with a key derived from `secrets.json`;
  the last used step is stored to reject replays; recovery codes are
  HMAC-SHA256 keyed hashes. Step-up password checks guard backups and 2FA
  changes.
- First run: while no admin exists, every start stores a fresh
  `sha256(token)` + 24 h expiry in `meta` and prints
  `Create your admin account: <url>/admin/setup#t=<token>`; setup consumes it
  in the same transaction that creates the admin.
- Every admin action goes to `admin_audit` (actor, action, target, detail,
  IP). Privacy: no admin endpoint returns moments, captions, messages or
  `/m/` URLs — avatars are streamed by the admin API itself;
  `TestAdminPrivacy` sweeps every GET route. The one deliberate exception is
  a member report's detail (`GET reports/{id}` + `media`/`thumb`), which
  returns only the reported item and is named explicitly in that test
  (`TestAdminReportDetailShowsOnlyReportedItem` checks nothing else leaks).
- Backups: a password-confirmed request mints a single-use, 2-minute,
  session-bound download token; the download runs `VACUUM INTO` a temp file
  and streams it.
- The SPA (`internal/adminui`) serves `/admin/assets/*` with
  `max-age=31536000, immutable`, `index.html` with `no-store`, and
  `index.html` for unknown client-side routes.

### Sign in with Apple / Google

- ID tokens are verified by hand: RS256 only, JWKS cached per Cache-Control,
  iss/aud/exp/iat with 2 min skew.
- Every Frame server accepts the same app audiences, so each token is bound
  to the server it was obtained for. The client's `raw_nonce` must be
  `frame1:<server_host>:<base64url(32 random bytes)>`.
  - `<server_host>`, the text between the prefix and the last `:`, must equal
    a configured public host from `FRAME_PUBLIC_URL` (a comma list) or
    `FRAME_DOMAIN`; the request `Host` header is never used.
  - The token's `nonce` must equal `sha256hex(raw_nonce)`.
  - `sha256(raw_nonce)` is stored in `used_nonces` until the token expires,
    so every nonce is single-use, even across restarts.
- Without a configured public host both methods are off (`/server` reports
  `false`). Dev mode accepts any host.

### Authorization model

- Feed is fan-out on write: a moment gets one `feed_entries` row per viewer,
  including the author.
- A non-author may see a moment only if their row exists **and** they are
  currently friends with the author. Unfriending or blocking revokes access
  immediately, and re-friending restores it.
- Every moment endpoint, every reply reference and every signed URL issued
  goes through that check. Media URLs are HMAC-signed capabilities, valid
  24–25 h; they are rounded to the hour so they stay cache-stable.
- Messages are only visible to their two participants.
- Unfriending or blocking records a `friend_removals` row. While it exists,
  redeeming the remover's old invite link only creates a normal friend request
  (`status: "requested"`), and a blocked redeemer gets `blocked`. The row is
  cleared when the two become friends again.
- Invite codes are stored as SHA-256 for lookup and AES-GCM encrypted (key
  derived from `secrets.json`) so `GET /invites` can list them.
- Password change revokes all other sessions and their devices.
- Revoked sessions' SSE streams close immediately for in-process
  revocations, and within one ping (25 s) for revocations made elsewhere,
  e.g. the admin CLI, because each ping re-validates the session.
- `TestAuthzMatrix` checks that a stranger, an ex-friend, a blocked user and a
  friend who isn't a recipient can't read or act on moments, media,
  reactions or messages.

### Schema (migrations `0001_init.sql` … `0005_reports.sql`)

| table | purpose |
|---|---|
| `users` | profile, identifiers (email/phone lowercase/E.164, unique), `password_hash` (Argon2id PHC), `apple_sub`/`google_sub`, privacy flags |
| `contact_hashes` | `sha256("frame-contact-v1:"+identifier)` of **discoverable** users only (uploaded hashes are never stored) |
| `sessions` | `token_hash` (SHA-256 of the 256-bit token), device name/platform, `last_seen_at` |
| `devices` | one push device per session (relay tokens, `push_key`, prefs); cascades with the session |
| `friendships` | both directions; `friend_requests` (unique from→to); `blocks` |
| `invites` | `code_hash` (SHA-256 of the 128-bit code), expiry |
| `media` | every blob: content type, bytes, dimensions, duration; storage key = media id |
| `moments` | author, kind, audience, media/thumb ids, `local_date`, caption columns (`caption_data` = compact JSON) |
| `feed_entries` | `(user_id, moment_id)` PK + `author_id`, `hidden`, `viewed_at` (doubles as "seen by"); indexes `(user_id, author_id, moment_id)` and partial `(user_id, author_id) WHERE viewed_at IS NULL AND hidden = 0` for unseen counts |
| `reactions`, `message_reactions` | one per user per object |
| `messages` | `conv` key (sorted user ids) indexed with id for paging; `read_at` (reader's unread state) vs `receipt_at` (what the sender sees, unset when the reader disabled read receipts); moment reply stored denormalised so deleted moments render `deleted: true` |
| `conversations` | per-user index (both directions) of last message and update time |
| `password_resets` | hashed 8-digit codes (15 min, 5 attempts each), when SMTP is configured; `reset_limits` caps confirms at 10 per user per 24 h |
| `used_nonces` | `sha256(raw_nonce)` of consumed Apple/Google nonces until the token's expiry (migration `0002`) |
| `friend_removals` | (remover, removed) after unfriend/block; restricts invite redemption |
| `server_invites` | admin invites: `code_hash`, sealed `code_enc`, `max_uses`/`uses`, optional `expires_at`, `revoked_at`, note (migration `0003`) |
| `users` (+0003) | `is_admin`, `disabled_at`, `invited_by`, `server_invite_id` |
| `admin_sessions` | panel sessions: `token_hash`, IP, user agent, created/last seen |
| `admin_totp`, `admin_recovery_codes` | sealed TOTP secret + last used step; keyed hashes of single-use recovery codes |
| `settings` | values saved in the admin panel (registration, user_invites, name, limits) |
| `admin_audit` | audit log of admin actions (no foreign keys, outlives users) |
| `reports` | member reports (migration `0005`): reporter, target type/id (no FK, so rows outlive the content), owner, reason, note, `status` open/resolved, `resolution`, `resolved_by`/`resolved_at`; one open report per reporter and target |

IDs are monotonic ULIDs, so `ORDER BY id` is chronological and cursors are
opaque `base64url("v1:"+id)`. A 30-moment feed page for a user with 200
friends and 50k moments (250k feed rows) takes ≈1.2 ms through the full
handler and ≈0.08 ms for the query alone. `TestFeedLatencyBudget` enforces
< 10 ms and checks the query plan.

### Media

- Moment uploads are multipart and read as a stream, so nothing is buffered
  whole.
  - The `meta` part is ≤ 64 KiB JSON and is checked first.
  - A retried `client_id` returns the original moment immediately (200).
  - `media` is spooled to `<data>/tmp`, up to `max_upload_bytes`.
  - `thumb` is ≤ 200 KiB.
- Types are checked by magic bytes only: JPEG `FF D8 FF`, HEIC `ftyp`
  heic/heix/heim/heis/mif1, MP4 any other video `ftyp` brand. The declared
  part type must agree.
- Images are never decoded. The only parsing is walking JPEG markers to the
  SOF header, so thumbs (≤ 512²) and avatars (≤ 1024²) can be dimension
  checked.
- Media rows are committed before their blob is written. Rows no moment or
  avatar references for more than 1 h are orphans, and a 10-minute sweeper
  deletes their blobs and rows. This covers crashes, rejected uploads and
  failed deletes.
- Deleting a moment, deleting an account (and its avatar), or replacing an
  avatar deletes the blobs immediately.
- `GET /m/{id}?e=&s=` verifies the HMAC (constant time) and the expiry, then
  serves via `http.ServeContent`. That gives Range/206, HEAD and
  `If-None-Match` (ETag = media id), with `Cache-Control: private,
  max-age=86400, immutable`.

### Storage

- **local** (the default) lives in `<data>/media/<hh>/<hh>/<id>`, sharded by
  SHA-256 of the key.
  - Writes are atomic: temp file, fsync, rename.
  - Files are 0600 and directories 0700.
- **s3** uses path-style URLs and SigV4 signing (PUT/GET with Range/HEAD/DELETE,
  payload SHA-256 signed).
  - It's tested against the official AWS SigV4 test-suite vectors and S3
    documentation examples, plus an httptest fake S3 that independently
    verifies signatures.
  - Works with R2 (`FRAME_S3_REGION=auto`), AWS and MinIO.

**Encryption at rest** (`FRAME_S3_ENCRYPT=true`, the default for s3) wraps
the S3 backend. The master key is `storage_key` in `secrets.json`; losing it
loses the media. Format version 1:

```
object  = header || chunk_0 || … || chunk_{n-1}
header  = "FRM1" || 0x01 (version) || 0x10 (log2 chunk size = 16) || 0x00 0x00      (8 bytes)
chunk_i = AES-256-GCM(K_obj, nonce_i, plaintext_i, aad = header)                  (≤ 64 KiB + 16)
K_obj   = HMAC-SHA256(master_key, "frame-storage-v1:" || media_id)                (per-object key)
nonce_i = 0x00×7 || uint32_be(i) || final_flag (0x01 on the last chunk)           (STREAM construction)
```

- Chunks can't be reordered, dropped or truncated without failing
  authentication.
- The plaintext length follows from the object length:
  `ciphertext_len = 8 + plaintext_len + 16·n`.
- Range reads fetch and decrypt only the chunks they touch, so video seeking
  stays cheap.
- Uploads are streamed: the ciphertext is a deterministic, seekable view of
  the plaintext spool file, so the SigV4 payload hash and the PUT both read
  it without buffering.
- Objects are write-once with fresh random ids, so `(K_obj, nonce)` pairs
  never repeat.

### Realtime (SSE)

- `GET /api/v1/events` subscribes the session to an in-memory hub that maps
  user to connections.
  - Every event for a user fans out to all their devices.
  - The stream opens with `: connected` and `retry: 3000`, then sends
    `: ping` every 25 s.
- A connection whose 64-event buffer is full is dropped rather than blocking
  publishers; clients reconnect and re-fetch, since there is no replay.
- There are at most 8 connections per user.
- Logout, session revocation and account deletion close the affected
  streams. Shutdown closes all of them before `http.Server.Shutdown`.

### Push fan-out (PUSH.md)

- Handlers call `notify(...)`, which **never blocks**. It does a non-blocking
  enqueue onto a bounded fan-out queue (1024), and drops are counted and
  logged.
- Two fan-out workers:
  - load the recipients' devices from SQLite
  - apply per-device prefs (moments/messages/reactions/friends)
  - build the plaintext payload once
  - seal it separately for each device:
    `base64(nonce12 || AES-256-GCM(push_key) || tag)`, matching
    `docs/test-vectors/push.json` (tested).
- The sealed jobs go onto a bounded send queue (4096) served by 4 workers
  that `POST {relay}/v1/push`.
- Retries cover network errors, 429 and 5xx: 3 attempts in total, with
  exponential backoff of 1 s ×1/×4 plus jitter, re-enqueued by timer so
  workers never sleep.
- Other 4xx responses are permanent failures. A `410` deletes the device
  (alert token) or clears its widget token.
- New moments also send `type:"widget"` pushes. They go to every device with
  a `widget_relay_token` (iOS 26+ WidgetKit), and to Android devices that
  muted moment alerts (via their FCM relay token), so widgets refresh even
  with notifications off.
- Collapse ids are `moment-<id>`, `reaction-<moment>-<user>` and
  `friend-<user>`; thread ids are `chat-<sender>`.
- On shutdown, queued pushes get up to the shutdown deadline to drain.

### TLS

- With `FRAME_DOMAIN` set, the server uses autocert. It serves `:443` with
  ACME TLS-ALPN, and `:80` serves HTTP-01 challenges and redirects everything
  else to HTTPS. Certificates are cached in `<data>/acme`.
- Without it, the server speaks plain HTTP on `FRAME_LISTEN`, for use behind
  a reverse proxy or tunnel.
- SIGINT/SIGTERM trigger a graceful shutdown with a 15 s budget.
