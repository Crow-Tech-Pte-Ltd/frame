# Frame admin panel

Every Frame server has a small web dashboard at **`/admin`** for the person who
runs it: invite people, manage accounts, change a few server settings, see how
much disk the server uses, and download a backup. The phone apps never use it.

> **Frame's admin panel doesn't show anyone's photos or messages.** It shows
> accounts, counts and sizes — never moments, captions, chats or reactions.
> The one exception is an item a member **reported** to you: the report shows
> that single moment, message or profile, because the member chose to share
> it with you ([Reports](#reports-and-the-content-exception)).
> (The operator can still read the database directly; see
> [SECURITY.md](SECURITY.md#1-trust-model). The panel simply doesn't make
> browsing people's content part of running a server.)

## Contents

1. [What you can do](#1-what-you-can-do)
2. [First run: create the admin account](#2-first-run-create-the-admin-account)
3. [Who can join](#3-who-can-join)
4. [Where the panel is reachable (`FRAME_ADMIN`, `FRAME_ADMIN_LISTEN`)](#4-where-the-panel-is-reachable)
5. [Security model](#5-security-model)
6. [Command-line equivalents](#6-command-line-equivalents)
7. [Admin API reference](#7-admin-api-reference)

---

## 1. What you can do

| Page | What it's for |
|---|---|
| **Overview** | Server name, version and uptime, registration mode, the number of open reports, user counts (total, active in the last 7 days, suspended, admins), number of moments and messages, media storage and database size, whether the push relay is reachable, the latest sign-ups and admin actions. |
| **Users** | Search, filter and sort accounts. Each row shows the avatar, name, username, a masked e-mail/phone, when they joined and were last seen, how many moments they posted, how much storage they use and how many friends they have. Open a user to see their signed-in devices (and sign them out), their invite links, and to **reset their password** (a one-time password is shown once), **suspend/unsuspend**, **make/remove admin**, or **delete** the account (you type the username to confirm). You can also **create an account** (needed when registration is `closed`). |
| **Reports** | What members reported to you from the app (a moment, a message or a profile), open reports first, with the reporter, the reported user, the type, the reason, the reporter's note and the date; the nav shows how many are open. Open a report to see the reported item and **delete it**, **suspend** its author, or **dismiss** the report. See [Reports](#reports-and-the-content-exception). |
| **Invites** | Create a **server invite** link (one use and 7 days by default, with an optional note like "for Mum"), copy it or show it as a QR code, see who joined with it, revoke it. Also lists the friend-invite links your members created, so you can revoke any of them. |
| **Settings** | Registration mode, whether members' invite links can bring in new people, server name, max video length, max upload size, max friends. Anything set in `frame.conf` / the environment is shown as **locked** ("set in frame.conf") and can't be changed here. |
| **Security** | Change your password, turn on two-factor authentication (an authenticator app such as Aegis, 1Password, Google Authenticator), get recovery codes, and see/sign out your admin sessions. |
| **Audit log** | Every admin action — who, what, on whom, when and from which IP. |
| **Maintenance** | Download a consistent database backup (asks for your password again) and run the orphaned-media cleanup now. |

The panel works on a phone, follows your system's light/dark setting (with a
manual switch), and loads nothing from other websites.

### Reports and the content exception

Members can report a moment, a message or a person from the app (API.md
"Reports"). Frame never scans anything automatically; a report is a person
asking **you**, the server's admin, to look at one thing.

- **What a report contains:** who reported, whose content it is, the type, a
  reason (spam, harassment, nudity, violence, self-harm, other), an optional
  note of up to 500 characters, and the date. Only ids are stored, never a
  copy of the content. Report rows stay when the content or either account is
  deleted later (the people then show as "Deleted account").
- **The deliberate exception:** opening a report shows **exactly the reported
  item** — the moment's photo or video and caption, the message's text, or the
  profile — and nothing around it (no other moments, no chat history). The
  member who reported it could already see it and chose to show it to you.
  Photos and videos stream through the admin API with your admin session
  (`reports/{id}/media`), not as shareable signed links, so they also work on
  a separate `FRAME_ADMIN_LISTEN` address. The report list and the overview
  never contain content. Opening a reported moment or message (its detail
  or its photo/video) is written to
  the audit log (`report.view`, at most once per admin and report per half
  hour).
- **Actions** (each recorded in the audit log):
  - **Delete** the moment or message, exactly as if its author had: it
    disappears for everyone, a moment's media is deleted too.
    (`report.delete_content`)
  - **Suspend** the author — the same suspension as in Users, which you can
    undo there. (`report.suspend_user`, plus `user.disable`)
  - **Dismiss** the report: nothing happens to anyone. (`report.dismiss`)

  An action resolves the report **and every other open report on the same
  item**, so duplicates don't pile up. You can't suspend yourself from a
  report.
- In the app, reporting a moment also hides it from the reporter's feed, and
  the reporter can block the author in the same step.

## 2. First run: create the admin account

A new server has no admin. On every start while that is true, `frame-server`
prints a one-time link to its log:

```
Create your admin account: https://frame.example.com/admin/setup#t=Xk3…
```

- Find it with `sudo journalctl -u frame | grep "admin account"` (systemd) or
  `docker compose logs frame | grep "admin account"` (Docker).
- Lost it, or it expired? Run `frame-server admin setup-link` (or
  `sudo frame-admin setup-link` with the systemd install) to print a fresh one.
  Each new link replaces the previous one.
- The link works **once** and expires after **24 hours**. The token is in the
  `#` part of the URL, which browsers never send to servers or proxies, and the
  server only stores a hash of it.
- The link uses `FRAME_ADMIN_LISTEN`'s address when that is set, else
  `FRAME_PUBLIC_URL`, else `http://localhost:<port>` — replace the host with
  however you reach the server.

The setup page asks for your name, a username, an e-mail address or phone
number, and a password. That creates a **normal Frame account that is also an
admin** — sign in with the same details in the iOS/Android app to use Frame
yourself. Afterwards, go to **Security** and turn on two-factor authentication;
the panel reminds you until you do.

More admins: open a user and choose **Make admin**, or run
`frame-server admin user promote <username>`. The last admin can't be removed.

## 3. Who can join

`FRAME_REGISTRATION` (or **Settings → Registration** when it isn't set in the
config file):

| Mode | Who can create an account |
|---|---|
| `invite` (**default**) | Only people with a valid invite: a **server invite** created in the panel (limited uses and expiry, no friendship), or — when **members can invite** (`FRAME_USER_INVITES`, on by default) — any member's friend-invite link from the app, which also makes them friends instantly. |
| `open` | Anyone who knows the server address. |
| `closed` | Nobody signs up; admins create accounts in the panel (**Users → New user**) or with `frame-server admin user create`. |

Signing in to an existing account never needs an invite. A **suspended**
account can't sign in (the app shows "account disabled"), all its devices are
signed out immediately (the apps say the account was suspended by the server
admin), and it no longer appears in search or contact
matching; its moments and messages stay until you delete the account.

## 4. Where the panel is reachable

| Setting | Effect |
|---|---|
| `FRAME_ADMIN=on` (default) | `/admin` is served next to the app API on the same address. |
| `FRAME_ADMIN=local` | `/admin` answers only to clients on loopback or in `FRAME_TRUSTED_PROXIES` networks; everyone else gets `404`. Behind a reverse proxy the real client IP is used, so the proxy itself doesn't count. A proxy or tunnel on the same host must be listed in `FRAME_TRUSTED_PROXIES` (for cloudflared, the `cloudflare` keyword): requests it forwards carry `X-Forwarded-For`/`CF-Connecting-IP`, and from an unlisted loopback peer those are refused rather than treated as local. |
| `FRAME_ADMIN=off` | No web panel at all (the CLI still works). |
| `FRAME_ADMIN_LISTEN=127.0.0.1:8081` | Serve the panel **only** on this separate address; the main address answers `/admin` with `404`. Combine with `FRAME_ADMIN=local` for belt and braces. |

### Reaching a private panel over SSH

With `FRAME_ADMIN_LISTEN=127.0.0.1:8081` on the server, from your laptop:

```sh
ssh -N -L 8081:127.0.0.1:8081 you@your-server
```

then open <http://localhost:8081/admin/>. Nothing about the panel is exposed
to the internet. (Browsers treat `localhost` as a secure context, so this
works without HTTPS.) With Docker, publish the admin port only on the host's
loopback: `ports: ["127.0.0.1:8081:8081"]` and `FRAME_ADMIN_LISTEN=:8081`.

Set `FRAME_PUBLIC_URL` when you use a tunnel, otherwise invite links created in
the panel would point at `localhost`.

## 5. Security model

The panel is a separate surface from the app API and is locked down on its own:

- **Who:** only accounts with the admin flag can sign in; everyone else gets
  "invalid credentials". Every request re-checks that the account is still an
  admin and not suspended, so removing an admin takes effect immediately.
- **Sessions:** a random 256-bit cookie `frame_admin` (stored only as a
  SHA-256 hash), `HttpOnly`, `SameSite=Strict`, `Path=/admin`, and `Secure`
  whenever the request arrived over HTTPS (directly or via a trusted proxy).
  Sessions end after **30 minutes idle** or **12 hours** at most. Admin
  sessions are separate from app sessions and are listed under Security.
- **Sign-in limits:** like the app — 10 attempts/minute per IP, 5/minute per
  identifier (e-mail, phone or username) and IP, 30/minute per identifier.
  Wrong passwords and wrong codes count the same. Two-factor codes (TOTP or
  recovery) are additionally limited to 10 attempts per 15 minutes per
  account, whichever identifier or IP they come from, so a known password
  doesn't make the second factor guessable.
- **Two-factor authentication (optional, recommended):** TOTP (RFC 6238:
  SHA-1, 6 digits, 30 s, ±1 step, each code usable once). The secret is
  stored encrypted with a key from `secrets.json`; the QR code is drawn in your
  browser. Eight single-use **recovery codes** are shown once and stored only
  as keyed hashes. Lost both? `frame-server admin user reset-2fa <username>`.
- **CSRF:** every state-changing request must carry the header
  `X-Frame-Admin: 1` **and** come from the panel's own origin (`Origin`
  matches, or `Sec-Fetch-Site: same-origin`); anything else gets `403`. Cookies
  are `SameSite=Strict` on top.
- **Content Security Policy:** `default-src 'self'; img-src 'self' data: blob:;
  style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none';
  form-action 'self'` — no inline scripts or styles, no third-party resources,
  no framing. Hashed assets are cached for a year; everything else is
  `no-store`.
- **Step-up:** downloading a backup and changing two-factor settings ask for
  your password again; deleting a user requires typing their username. You
  can't suspend, demote or delete yourself, and the last admin can't be
  demoted.
- **Setup link:** single-use, 24 h, hashed at rest, in the URL fragment;
  rejected as soon as any admin exists.
- **Audit log:** every admin action (and every panel sign-in) is recorded with
  the acting admin, the target, the time and the IP. CLI actions are recorded
  as `CLI`. Entries never contain passwords, codes or tokens.
- **Privacy by design:** the admin API has no endpoint that returns moments,
  media URLs, captions, messages or reactions — only counts and metadata
  (tests enforce this). Avatars are served through the admin API itself. The
  single, deliberate exception is a report's detail (`GET reports/{id}` and its
  `media`/`thumb`), which returns only the item a member reported; the test
  that forbids content everywhere else names exactly those routes.
- **Reset passwords** produce a one-time password shown once (copy it to the
  person over a channel you trust); all of that user's devices are signed out.

## 6. Command-line equivalents

```
frame-server admin setup-link                          one-time link to create the first admin
frame-server admin user list                           (shows ADMIN and SUSPENDED columns)
frame-server admin user create --email E | --phone P [--name N] [--username U] [--admin]
frame-server admin user promote <username>             make an existing user an admin
frame-server admin user demote <username>              (refuses to remove the last admin)
frame-server admin user reset-2fa <username>           turn off an admin's two-factor authentication
frame-server admin user reset-password <username|email|phone>
frame-server admin user unlink <username|email|phone> apple|google   remove a linked sign-in, sign out everywhere
frame-server admin user delete <username>
frame-server admin invite create <username>            friend invite of that user
frame-server admin invite server [--uses N] [--days D] [--note TEXT]   server invite
```

With the systemd install, use the `frame-admin` wrapper (`sudo frame-admin …`);
with Docker, `docker compose exec frame frame-server admin …`.

---

## 7. Admin API reference

The panel's JSON API lives under **`/admin/api/v1/`**. It is not part of the
app contract ([API.md](API.md)), may change between server versions, and is
only meant for the bundled panel.

Conventions as in API.md: JSON in/out, timestamps in ms, errors
`{"error":{"code","message"}}`, `null` instead of omitted fields.

- **Auth:** cookie `frame_admin` (set by `login`/`setup`). Missing/expired →
  `401 unauthorized`; signed in but no longer an admin → `403 forbidden`.
- **CSRF:** every non-GET/HEAD request needs `X-Frame-Admin: 1` and a same-origin
  `Origin` (or `Sec-Fetch-Site: same-origin`) → otherwise `403 csrf_failed`.
- **Errors used:** `bad_request` 400, `confirm_mismatch` 400, `unauthorized` 401,
  `invalid_credentials` 401, `totp_required` 401, `totp_invalid` 401,
  `forbidden` 403, `csrf_failed` 403, `setup_token_invalid` 403, `not_found` 404,
  `setup_done` 409, `setting_locked` 409, `last_admin` 409, `self_action` 409,
  `content_gone` 409,
  `identifier_taken` 409, `username_taken` 409, `rate_limited` 429.

### Objects

```jsonc
// UserMini
{"id": "01J…", "username": "alice", "display_name": "Alice"}

// AdminUser (list row)
{
  "id": "01J…", "username": "alice", "display_name": "Alice",
  "has_avatar": true,                      // GET users/{id}/avatar serves it
  "email_masked": "a•••e@example.com",     // or null
  "phone_masked": "+34•••••••222",         // or null
  "auth_methods": ["password", "apple"],
  "is_admin": false,
  "disabled_at": null,                     // ms when suspended
  "created_at": 1730000000000,
  "last_seen_at": 1730000000000,           // newest app session activity, or null
  "moments": 12, "storage_bytes": 1234567, "friends": 4
}

// AdminUserDetail = AdminUser + 
{
  "email": "alice@example.com", "phone": null,   // full values (detail only)
  "discoverable": true, "searchable": true,
  "totp_enabled": false,                          // admins only meaningful
  "invited_by": UserMini | null,                  // joined via a member's link
  "server_invite": {"id": "01J…", "note": "for Mum"} | null
}

// AppSession
{"id", "device_name", "platform", "created_at", "last_seen_at", "push": true}

// FriendInvite (a member's link; the code itself is never returned)
{"id": "<hex>", "owner": UserMini, "created_at", "expires_at", "uses"}

// ServerInvite
{
  "id": "01J…",
  "code": "<22 chars>" | null,      // only while status == "active"
  "url": "https://frame.crow.sg/i#s=…&c=…" | null,
  "note": "", "max_uses": 1, "uses": 0,
  "created_at", "expires_at": ms | null, "revoked_at": ms | null,
  "status": "active" | "used_up" | "expired" | "revoked",
  "created_by": UserMini | null,
  "registered": [{"user": UserMini, "at": ms}]
}

// ReportUser
{"id": "01J…", "username": "alice", "display_name": "Alice", "has_avatar": true, "suspended": false}

// AdminReport (list row and detail; never content)
{
  "id": "01J…",
  "target_type": "moment" | "message" | "user",
  "content_exists": true,                  // false once deleted
  "reason": "spam" | "harassment" | "nudity" | "violence" | "self_harm" | "other",
  "note": "…" | null,                      // the reporter's words, ≤ 500 chars
  "created_at": ms,
  "status": "open" | "resolved",
  "resolution": "deleted_content" | "suspended_user" | "dismissed" | null,
  "resolved_at": ms | null, "resolved_by": UserMini | null,
  "reporter": ReportUser | null,           // null once the account is deleted
  "reported_user": ReportUser | null       // author / sender / the reported user
}

// ReportContent (GET reports/{id} only — exactly the reported item)
{"type": "moment", "id", "kind": "photo"|"video", "created_at",
 "caption": {"type", "text"} | null,
 "media": {"url": "/admin/api/v1/reports/<id>/media", "content_type", "width", "height", "duration_ms"} | null,
 "thumb": {"url": "/admin/api/v1/reports/<id>/thumb"}}
| {"type": "message", "id", "created_at", "text"}
| {"type": "user", "id", "created_at", "user": ReportUser}

// AuditEntry
{"id", "at", "actor": {"id": "01J…" | null, "label": "alice" | "CLI" | "setup"},
 "action": "user.disable", "target": {"type": "user", "id", "label"} | null,
 "detail": "…" | null, "ip": "203.0.113.4"}

// Session (login/setup/session responses)
{
  "admin": {"id", "username", "display_name", "email", "phone", "totp_enabled", "recovery_codes_left"},
  "server": {"name", "version", "public_url": string | null},
  "session": {"id", "created_at", "expires_at", "idle_timeout_ms": 1800000}
}
```

### Endpoints

No session needed:

| Method & path | Body → response |
|---|---|
| `GET setup` | → `{"needed": bool, "server_name"}` |
| `POST setup/verify` | `{"token"}` → `204` · `403 setup_token_invalid` · `409 setup_done` |
| `POST setup` | `{"token","display_name","username","method":"email"\|"phone","identifier","password"}` → Session (+cookie) |
| `POST login` | `{"identifier","password","totp_code":null}` (identifier = e-mail, phone or username; `totp_code` = 6 digits or a recovery code) → Session (+cookie) · `401 invalid_credentials` · `401 totp_required` · `401 totp_invalid` |
| `GET session` | → Session · `401` |
| `POST logout` | → `204` (clears the cookie) |

Session required:

| Method & path | Body → response |
|---|---|
| `GET overview` | → `{"server":{"name","version","started_at","registration","user_invites","storage":"local"\|"s3","public_url","admin_mode":"on"\|"local"\|"off","admin_listen":bool},"users":{"total","active_7d","disabled","admins"},"content":{"moments","messages"},"reports":{"open"},"storage":{"media_bytes","media_files"},"database":{"bytes"},"push":{"configured","relay_url","reachable":bool\|null,"checked_at":ms\|null},"recent_signups":[AdminUser],"recent_actions":[AuditEntry]}` |
| `GET users?q=&status=all\|active\|disabled\|admins&sort=created_at\|last_seen_at\|display_name\|moments\|storage_bytes\|friends&order=desc\|asc&page=1&per_page=25` | → `{"users":[AdminUser],"total","page","per_page"}` (`q` matches name, username, e-mail, phone) |
| `POST users` | `{"display_name","username":null,"method":"email"\|"phone","identifier","admin":false}` → `201 {"user":AdminUser,"password":"<one-time>"}` |
| `GET users/{id}` | → `{"user":AdminUserDetail,"sessions":[AppSession],"invites":[FriendInvite]}` |
| `GET users/{id}/avatar` | → JPEG bytes (`404` if none) |
| `POST users/{id}/reset-password` | → `{"password":"<one-time>"}` (signs out all of the user's devices) |
| `POST users/{id}/disable` · `POST users/{id}/enable` | → `204` (`409 self_action` for yourself) |
| `POST users/{id}/promote` · `POST users/{id}/demote` | → `204` (`409 last_admin`, `409 self_action`) |
| `DELETE users/{id}/sessions/{session_id}` · `DELETE users/{id}/sessions` | → `204` |
| `DELETE users/{id}` | `{"confirm":"<username>"}` (the user id when they have no username) → `204` · `400 confirm_mismatch` · `409 self_action` |
| `GET invites?status=active\|all` | → `{"invites":[ServerInvite]}` |
| `POST invites` | `{"max_uses":1,"expires_in_days":7,"note":""}` (`max_uses` 1–1000; `expires_in_days` 1–365 or `null` = never) → `201 {"invite":ServerInvite}` |
| `DELETE invites/{id}` | → `204` (revoke) |
| `GET friend-invites` | → `{"invites":[FriendInvite]}` (all members' unexpired links) |
| `DELETE friend-invites/{id}` | → `204` |
| `GET settings` | → `{"settings":{"registration":S,"user_invites":S,"name":S,"max_video_seconds":S,"max_upload_mb":S,"max_friends":S}}` with `S = {"value", "default", "locked": bool, "source": "default"\|"panel"\|"config"}` |
| `PATCH settings` | any subset of `{"registration","user_invites","name","max_video_seconds","max_upload_mb","max_friends"}`; `null` resets to the default → same as `GET` · `409 setting_locked` |
| `GET reports?status=all\|open\|resolved&page=1&per_page=25` | → `{"reports":[AdminReport],"total","open","page","per_page"}` (open first, then newest; `open` counts all open reports) |
| `GET reports/{id}` | → `{"report":AdminReport,"content":ReportContent\|null}` (`null` once the item is gone; opening a moment/message logs `report.view`) |
| `GET reports/{id}/media` · `GET reports/{id}/thumb` | → the reported moment's photo/video or thumbnail bytes (one `Range` per request, `no-store`; logged as `report.view`) · `404` for other report types · `409 content_gone` |
| `POST reports/{id}/delete-content` | → `{"resolved": n}` (deletes the moment/message as its author would; resolves every open report on it) · `400` for user reports · `409 content_gone` |
| `POST reports/{id}/suspend-user` | → `{"resolved": n}` (suspends the author unless already suspended) · `404` if the account is gone · `409 self_action` |
| `POST reports/{id}/dismiss` | → `{"resolved": n}` |
| `GET audit?page=1&per_page=50` | → `{"entries":[AuditEntry],"total","page","per_page"}` |
| `POST maintenance/backup` | `{"password"}` → `{"url":"/admin/api/v1/maintenance/backup/<one-time token>","expires_at"}` |
| `GET maintenance/backup/{token}` | → `application/vnd.sqlite3` attachment `frame-backup-<date>.db` (single use, 2 min, same admin session) |
| `POST maintenance/cleanup` | → `{"orphan_media_removed","expired_invites_removed"}` |
| `GET security` | → `{"totp_enabled","recovery_codes_left","sessions":[{"id","ip","user_agent","created_at","last_seen_at","current"}]}` |
| `DELETE security/sessions/{id}` | → `204` |
| `POST security/password` | `{"current_password","new_password"}` → `204` (signs out your other admin sessions and all your app devices) |
| `POST security/totp/setup` | `{"password"}` → `{"secret":"BASE32","uri":"otpauth://totp/…"}` (pending until enabled) |
| `POST security/totp/enable` | `{"code"}` → `{"recovery_codes":["xxxxx-xxxxx", …8]}` |
| `POST security/totp/disable` | `{"password","code"}` → `204` |
| `POST security/recovery-codes` | `{"password"}` → `{"recovery_codes":[…8]}` (replaces the old ones) |

The SPA itself is served from `/admin/` (any unknown `/admin/…` path returns
`index.html`); `/admin/assets/*` are content-hashed and cached immutably.
