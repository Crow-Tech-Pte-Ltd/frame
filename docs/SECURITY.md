# Frame security model

Frame is built to be **secure by being small**: few moving parts, few
dependencies, and nothing that parses untrusted media on the server. This
document lists who is trusted, who isn't, which controls exist, and what Frame
deliberately does *not* try to do. Specifications referenced here:
[API.md](API.md), [PUSH.md](PUSH.md), [CONFIG.md](CONFIG.md),
[ADMIN.md](ADMIN.md).

## 1. Trust model

| Party | Trusted? | What they can see or do |
|---|---|---|
| **The server operator** (whoever runs `frame-server` for a group) | **Yes, fully** | Everything stored on that server: photos, videos, captions, messages, reactions, friend lists, account e-mail and phone numbers, IP addresses. This is the self-hosting deal: use a server run by someone you trust, or run your own. |
| **Other users on the same server** | No | Only what the authorisation model lets them see (section 3.4): moments addressed to them by friends, chats with friends, public profiles (name, username, avatar). |
| **Network attackers** (Wi-Fi, ISP, on-path) | No | With HTTPS: endpoints, timing and sizes only. |
| **The push relay operator** (Frame publisher, `frame.crow.sg`) | No | Opaque relay tokens, ciphertext sizes, timing, and which server IPs push. No content, no user identities (section 3.9). |
| **Apple / Google push services** | No | Device tokens, timing, ciphertext and the generic text "Frame: New activity". |
| **Object-storage provider** (e.g. Cloudflare R2) | No | AES-256-GCM ciphertext, object sizes and access timing (section 3.7). |
| **Tunnel / CDN provider** (e.g. Cloudflare Tunnel, if used) | Partially | It terminates TLS, so it can technically see the traffic. Use built-in HTTPS (`FRAME_DOMAIN`) to avoid this. |
| **Someone holding a stolen or borrowed phone** | No | See section 4. |

Assets, roughly in order of sensitivity:

1. Photos, videos and messages.
2. Account credentials and sessions.
3. The social graph and activity metadata.
4. Contact-matching hashes.
5. Push tokens.

## 2. Architecture in one paragraph

Each group of friends uses one `frame-server`: a single static Go binary with
SQLite and either local media or an S3/R2 bucket. The iOS and Android apps talk
to it over HTTPS (JSON + Server-Sent Events). For notifications, the server
encrypts a small payload with a key that **the device** generated, and hands it
to the stateless `frame-relay`. The relay forwards the opaque blob to
APNs/FCM. The relay also hosts the static invite page and the app-link
association files.

## 3. Controls

### 3.1 Transport

- HTTPS is required for anything on the internet. The server gets Let's
  Encrypt certificates itself (`FRAME_DOMAIN`, via `acme/autocert`), or sits
  behind Caddy or Cloudflare Tunnel.
- The apps refuse plain `http://` except for `localhost`, `10.0.2.2`,
  private-LAN and `.local` hosts, for development (API.md, Conventions).
- frame-server sends HSTS when it terminates TLS itself (`FRAME_DOMAIN`) or
  sits behind a trusted proxy or tunnel that did (`X-Forwarded-Proto` /
  `CF-Visitor`); the relay and the reference Caddy configuration send it
  too.

### 3.2 Passwords and sign-in

- Passwords are hashed with **Argon2id** (memory-hard; the parameters are
  stored with each hash so they can be raised later). Comparisons are
  constant-time.
- Login failures always return `invalid_credentials`, and an unknown
  account costs the same Argon2id time as a real one, so sign-in can't be
  used to enumerate accounts. Registration necessarily answers
  `identifier_taken` for an e-mail or phone that is already in use; on
  invite-only servers (the default) only someone holding a valid invite gets
  that far.
- Auth endpoints are rate limited: 10/min per IP, 5/min per identifier and
  IP, and 30/min per identifier from anywhere (high enough that an attacker
  elsewhere can't lock the owner out).
- **Sign in with Apple and Google:** the server verifies the RS256 signature
  against the provider's JWKS, plus `iss`, `aud` (only the configured app or
  Services IDs), `exp`, and a **server-bound, single-use nonce** (section
  3.2.1).
- A provider e-mail that matches an existing e-mail account is **not**
  auto-linked. This prevents account takeover through an attacker-controlled
  identity provider.
- Without SMTP, password resets go through the operator's admin CLI, which
  prints a one-time password. Changing a password revokes all other sessions.

#### 3.2.1 Cross-server ID-token replay

**The threat.** Every Frame server accepts the same app audiences: the official
iOS bundle id, the Android Services ID and the Google client IDs. So an Apple
or Google ID token that a user obtains to sign in to server **A** would be
equally valid at server **B**. A malicious operator of A could:

1. persuade a victim to sign in to A with Apple or Google;
2. capture the ID token, together with its `raw_nonce` if a plain nonce were
   used;
3. replay both to B, the victim's real server, within the token's lifetime;
4. be signed in to B **as the victim**.

**The mitigation** (API.md v1.1). The app builds the nonce as
`raw_nonce = "frame1:" + <server host> + ":" + <32 random bytes>`, and the
provider signs `sha256(raw_nonce)` into the token. The server:

- requires `raw_nonce` and checks that `sha256(raw_nonce)` matches the
  token's `nonce`;
- accepts it only if `<server host>` is one of **its own configured public
  hosts** (`FRAME_PUBLIC_URL` / `FRAME_DOMAIN`), never the request's `Host`
  header, which an attacker controls;
- remembers each `raw_nonce` until the token expires, so every nonce works
  **once**.

A token obtained for A therefore names A, and B rejects it. Replaying a token
against the server it was issued for also fails, because the nonce has already
been used.

**Consequences for operators.** Servers without a configured public URL
disable Apple and Google sign-in (`/server` reports them as `false`), which is
why every recipe in SELF-HOSTING.md sets `FRAME_PUBLIC_URL`. Dev mode accepts
any host.

### 3.3 Sessions

- Session tokens are long random **opaque** values (not JWTs). The server
  stores only their **SHA-256 hash**, so a leaked database doesn't yield
  usable sessions.
- Sessions are revocable individually (Settings → Sessions,
  `DELETE /auth/sessions/{id}`). Logging out revokes the session and its push
  registration.

### 3.4 Authorisation

- Every object access is checked against the requesting user:
  - Moments are visible only to their author and to recipients who are the
    author's friends.
  - Chats exist only between friends; after unfriending they become
    read-only.
  - Blocks remove friendship and hide each user from the other's searches.
- Invisible objects return **404, not 403**, so their existence isn't
  revealed.
- **Reports** (`POST /reports`, API v1.3) go through the same checks: a member
  can only report a moment, message or user they can see, and anything else
  is the same 404 as a missing id, so reporting can't be used to probe for
  content. Notes are length-limited (500 characters), reports are
  rate-limited (30/hour/user), and only ids are stored, never a copy of the
  content.
- The server's integration tests include negative cases: a stranger can't
  read a moment, its media, or a chat.
- Default per-user rate limits apply to writes (60 moments/hour,
  60 messages/minute) and everything else (600/minute). Contact matching is
  limited to 10 calls/hour.
- **Client IPs behind proxies.** Per-IP limits use `X-Forwarded-For` only from
  proxies listed in `FRAME_TRUSTED_PROXIES`. The `cloudflare` keyword trusts
  `CF-Connecting-IP` only from loopback and the listed CIDRs, so a client that
  reaches the server directly can't pick its own rate-limit identity. The
  Docker Compose files pin their network subnets for exactly this purpose.
- With `FRAME_ADMIN=local`, a request that carries forwarding headers
  (`X-Forwarded-For`, `Forwarded`, `CF-Connecting-IP`, …) from a loopback
  peer that isn't a listed proxy is refused: a reverse proxy or tunnel on
  the same host that the operator forgot to list doesn't make every visitor
  "local".

### 3.5 Media URLs

- Media is fetched as `/m/{media_id}?e=<expiry>&s=<HMAC-SHA256(server key, id.expiry)>`.
- The URL is a **24-hour capability**: it needs no `Authorization` header, so
  widgets and video players can load it. Anyone who obtains the URL can fetch
  that one file until it expires (see non-goals).
- One `Range` per request (video seeking). A multi-range request gets the
  whole file, so one URL can't make the server issue hundreds of storage
  reads (ranged GETs against S3/R2) per request.
- Responses use the exact stored `Content-Type`,
  `X-Content-Type-Options: nosniff`, and `Cache-Control: private`, so shared
  caches and CDNs don't keep copies.

### 3.6 No server-side media processing

- The server **never decodes** images or video. It checks magic bytes (JPEG,
  HEIC, MP4) and size limits, and stores the bytes.
- This removes the whole class of image/video-parser vulnerabilities from the
  server: decompression bombs, and ImageMagick- or ffmpeg-style remote code
  execution.
- Thumbnails are produced on the device.

### 3.7 Metadata stripping and storage encryption

- **On the device:** the apps re-encode photos and videos from pixels before
  upload, which drops EXIF/GPS and other metadata. The server can't enforce
  this (it doesn't decode media), so a modified client could upload metadata.
  Official apps don't.
- **S3/R2 at rest:** with `FRAME_S3_ENCRYPT=true` (the default), every object
  is encrypted with AES-256-GCM on the server before upload. The key lives in
  `secrets.json` (mode 0600) in the data directory. A leaked bucket or a
  curious provider sees only ciphertext.
- **Local disk:** files are stored as uploaded. Use full-disk encryption on
  the host if that matters to you.
- `secrets.json` also holds the media-URL signing key. **Back it up and keep
  it private.** Losing it makes encrypted R2 media unrecoverable. Leaking it
  lets someone mint media URLs for media ids they know and, together with a
  copy of the database, read stored invite codes and admins' TOTP secrets
  (their encryption keys are derived from it).

### 3.8 Invites and contact discovery

- Invite links look like `https://frame.crow.sg/i#s=<server>&c=<code>`. The
  server address and the code are in the URL **fragment**, which browsers
  never send to `frame.crow.sg`.
- The landing page is static, with a strict Content-Security-Policy
  (`script-src 'self'`, no inline script, no third-party resources) and no
  analytics.
- Codes are 22-character random base64url values (about 128 bits). A
  member's link expires after 30 days and can be used several times; an
  admin's server invite has the uses and expiry the admin chose (by default
  one use within 7 days). **Anyone holding an invite can
  become the inviter's friend**, so share it privately.
- **Revocation.**
  - The inviter can list their open invites and revoke them. The app's "Reset
    invite link" revokes them all and issues a new one. A revoked or expired
    code is rejected at once, including for registration on invite-only
    servers.
  - A redeemer whom the inviter previously **unfriended or blocked** doesn't
    get instant friendship from an old or forwarded link. For them the link
    only creates a normal friend request the inviter must accept, and a
    blocked user is refused.
  - Friendships that were already created through an invite are not undone by
    revoking it. Unfriend or block the person instead.
- Contact matching uploads SHA-256 hashes of normalised phone numbers or
  e-mails with a fixed prefix. The server doesn't store them, and only users
  who opted in (`discoverable`) can match.
- Be aware that phone-number hashes are guessable by brute force, so this
  protects against casual exposure, not a determined server operator. The
  operator is trusted anyway.

### 3.9 Push notifications

- **End-to-end encrypted payloads.** Each device generates a random 32-byte
  `push_key` and shares it only with its own server (over HTTPS, stored
  server-side). Notification text and thumbnail paths are encrypted with
  AES-256-GCM under that key.
  - On iOS, the Notification Service Extension decrypts them.
  - On Android, the FCM service does.
  - APNs/FCM and the relay see only ciphertext and the fallback text "Frame /
    New activity".
- **Sealed device tokens.** Servers never see raw APNs/FCM tokens, only a
  `relay_token`: `base64url(nonce ‖ AES-256-GCM(RELAY_KEY, {platform, token, app, kind}))`.
  - Servers can't extract device tokens.
  - Servers can't forge tokens to push to arbitrary devices.
  - Servers can't target apps outside the relay's allowlist.
  - Tampered tokens fail authentication.
- **Relay tokens expire after 60 days.** The sealed claims include an expiry
  (`e`), and expired tokens get `410`, so the server deletes the device.
  - The apps fetch a fresh token on every launch and hand it to their server,
    so devices in use are unaffected.
  - Revocation is bounded. A server the user has left, by uninstalling the app
    or abandoning the account, loses the ability to push to that device within
    60 days.
  - Logging out or revoking the session removes the device on the server
    immediately.
  - `RELAY_KEY` rotation, via a key list, invalidates everything sealed with a
    retired key.
- **Stateless, content-blind relay.**
  - It has no database and writes no files.
  - Logs contain only counters and provider status codes. Device tokens,
    relay tokens, payloads, IP addresses and Apple id_tokens are never logged,
    which is covered by a test.
  - Rate limits: 60 pushes/min per device and 600 requests/min per client IP.
  - Request bodies are limited, and upstream timeouts are 10 s.
- **What the relay operator could still learn or do:**
  - Learn which server IPs push, how often, to how many opaque devices, and
    when.
  - Drop or delay notifications.

  They **cannot** read content or learn who the users are.
- **Turning push off.** Operators can disable push entirely with
  `FRAME_PUSH_RELAY=`.
- **Sign in with Apple on Android.** Apple's web flow posts the `id_token` to
  `https://frame.crow.sg/apple/callback`.
  - The relay renders a no-store page that hands the values to the app via
    `frame://auth/apple#…`. Every value is percent-encoded, the CSP is strict,
    the `code` parameter is dropped, and nothing is stored or logged.
  - A captured `id_token` is useless without the `raw_nonce`, which never
    leaves the phone, and the app checks `state`.

### 3.9.1 Web admin panel

The operator's dashboard at `/admin` is a separate surface with its own
cookie sessions (HttpOnly, SameSite=Strict, 30 min idle / 12 h), CSRF checks,
a strict CSP, optional TOTP two-factor authentication (code attempts limited
per account, 10 per 15 minutes), an audit log, and a
one-time first-run setup link. It can be limited to local networks
(`FRAME_ADMIN=local`) or a separate loopback listener (`FRAME_ADMIN_LISTEN`).
By design it never shows photos, captions or messages, with one deliberate
exception: a member's **report** shows the admin that single reported item
(the member chose to share it). Its media is streamed through the admin API
behind the admin session rather than as a signed URL, opening it (the detail
or its photo/video) is audit-logged, and a test pins the exception to exactly
those routes. Details:
[ADMIN.md §5](ADMIN.md#5-security-model) and
[Reports](ADMIN.md#reports-and-the-content-exception).

### 3.10 Minimal dependencies and supply chain

- **Server:** the Go standard library plus `modernc.org/sqlite` (pure Go) and
  `golang.org/x/crypto` (Argon2id, ACME). There is no web framework, ORM or
  JWT library. Release binaries are static (`CGO_ENABLED=0`).
- **Admin panel:** a small React SPA built ahead of time and embedded in the
  binary; its npm dependencies are pinned exactly with a committed lockfile
  (`admin-ui/`), and it loads nothing from third-party origins.
- **Relay:** the Go standard library only.
- **Apps:** zero third-party packages on iOS. On Android, a small set of
  AndroidX/Google libraries.
- Releases ship a `SHA256SUMS` file, and `deploy/install.sh` **refuses to
  install** a binary or support file whose checksum doesn't match. This
  catches corrupted or altered downloads; it is not a signature, since the
  checksums come from the same GitHub release. The docs never use
  `curl | sh`: download, read, then run.
- `go.mod` names the exact Go toolchain (patch release) for server and relay,
  and the Dockerfiles pin the same release, so builds carry a patched
  standard library instead of whatever Go happens to be installed or
  cached.

### 3.11 Deployment hardening

**systemd** (`deploy/frame.service`):

- `DynamicUser=yes` and `StateDirectory=frame`: a throwaway system user that
  can write only its data directory.
- `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`.
- `NoNewPrivileges`, an empty `CapabilityBoundingSet` (`CAP_NET_BIND_SERVICE`
  is added by a drop-in **only** when `FRAME_DOMAIN` needs ports 80/443).
- `MemoryDenyWriteExecute`.
- `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`, `RestrictNamespaces`.
- `SystemCallFilter=@system-service ~@privileged`, `ProtectKernel*`,
  `ProtectProc=invisible`, `UMask=0077`.
- The configuration file is root-only (0600) and read by systemd before
  privileges are dropped.

**Docker** (`deploy/Dockerfile`, compose files):

- distroless/static base: no shell, no package manager.
- Runs as uid 65532 with a read-only root filesystem.
- `cap_drop: [ALL]` and `no-new-privileges`.
- In the Caddy variant, the app container publishes no ports.

**Logging:**

- The server never logs passwords, tokens or message text.
- frame-server's own access log records route patterns, never paths or
  query strings. Invite codes are path segments (`/api/v1/invites/{code}`),
  so a reverse proxy's or tunnel's access log does contain them.
- The reference Caddy configuration drops the signature (`s`) and expiry (`e`)
  query parameters and the `Authorization` header from access logs.

## 4. Explicit non-goals, known limitations and accepted risks

- **No end-to-end encryption of photos or messages against the server
  operator.** The operator can read everything on their server. This is the
  self-hosting trust model, and it keeps the system simple, fast and
  recoverable (backups, new devices, widgets). Only use servers run by people
  you trust.
- **Metadata is visible to the operator:** who is friends with whom, who
  posted when and to whom, reactions, views and read receipts, and IP
  addresses.
- **Recipients can keep what you send.** Screenshots, saving to Photos, and
  widget and notification caches can't be prevented.
- **Signed media URLs are bearer capabilities for 24 hours.** If a URL leaks
  (for example, pasted somewhere), that file can be fetched until it expires.
  Deleting a moment deletes the file, which ends access immediately.
- **Deletion is best-effort beyond the server:**
  - Earlier backups still contain deleted content.
  - Recipients' devices may keep cached copies until their caches expire.
- **Stolen or borrowed phone.**
  - Session tokens live in the iOS Keychain / Android Keystore-backed storage.
    An unlocked phone gives full access to the account.
  - Home-screen and lock-screen widgets show the latest photos to whoever
    holds the phone.
  - Whoever holds a signed-in session can also link their own Apple or
    Google account to it, or set a password on an account that has none,
    without knowing any password. Revoking sessions or changing the password
    does **not** undo a linked identity.
  - Mitigation: use a device passcode; revoke the session from another
    device, or ask the operator to reset the password. If an unknown Apple
    or Google sign-in was linked, the operator removes it with
    `frame-server admin user unlink <user> apple|google`, which also signs
    the account out everywhere.
- **Not anonymous.** The server operator (and Cloudflare, if you use a
  tunnel) sees IP addresses. Apple and Google learn that a Frame notification
  was delivered to a device at a given time.
- **No automatic moderation or content scanning,** by design. Members can
  block people and **report** a moment, message or person; reports go only to
  that server's own operator, who reviews them in the admin panel (delete the
  content, suspend or delete the account, or dismiss). Nothing is sent to the
  Frame publisher or any third party.
- **Availability.** A small self-hosted server can be overwhelmed by a
  determined attacker. Rate limits protect against abuse, not volumetric DDoS.
- **Federation.** Servers don't talk to each other. A user on server A can't
  see server B's content.

### Accepted risks

- **E-mail addresses and phone numbers are not verified.** Frame sends no
  SMS, and e-mail is only used for resets when SMTP is configured. On an
  `open` server, someone could therefore register **a victim's phone number
  or e-mail first**. Contact matching would then suggest the impostor's
  account to people who have the victim in their contacts.
  - **Mitigations:**
    - A suggestion is only a suggestion: friend requests still need to be
      **accepted**, and they show the requester's **username**, display name
      and avatar.
    - Nothing is shared until both sides are friends.
    - Servers are **invite-only by default** (`FRAME_REGISTRATION=invite`),
      which makes this practically impossible: only people holding an invite
      from the admin or (if allowed) a member can register. `closed` goes
      further.
    - The real owner can ask the operator to delete the impostor account.
  - **Why accepted:** verifying phone numbers would require a paid SMS
    provider, which conflicts with free, simple self-hosting.
- **Relay token lifetime.** A server you have left can still notify your
  device for up to 60 days (section 3.9). The content is encrypted with a key
  that server already has. Uninstalling the app, or turning off notifications
  for Frame, stops this immediately on the device side.

## 5. Operating the public relay (`frame.crow.sg`)

The relay is operated by Crow Tech, the app publisher. Its secrets
(`RELAY_KEY`, the APNs `.p8` key) are kept out of source control and are only
available to the relay process. Its protocol is specified in
[PUSH.md](PUSH.md).

- Rotating `RELAY_KEY` uses a key list, so outstanding tokens keep working.
- Retired APNs keys are revoked in the Apple portal.
- Self-hosters who don't want to use it can turn push off
  (`FRAME_PUSH_RELAY=`); the apps then fall back to periodic refresh.

## 6. Reporting a vulnerability

Please report security issues privately via **GitHub private vulnerability
reporting** on [`Crow-Tech-Pte-Ltd/frame`](https://github.com/Crow-Tech-Pte-Ltd/frame/security/advisories/new)
("Security" tab → "Report a vulnerability"), or by e-mail to
`hello@crow.sg` (subject "Security"). Don't open public issues for vulnerabilities.

We aim to acknowledge reports within **3 working days** and to agree a
disclosure timeline with you, typically up to 90 days. We'll credit reporters
who want to be credited. There is currently no bug bounty.

Please include affected versions/components (server, relay, iOS, Android),
reproduction steps, and the impact you observed. Test only against servers you
run yourself.
