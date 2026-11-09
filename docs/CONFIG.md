# frame-server configuration

Configuration comes from environment variables, optionally loaded from a
`KEY=value` file passed with `--config /path/frame.conf` (lines starting with
`#` are comments; real env vars override the file). Defaults are chosen so that
`frame-server` with **no configuration at all** runs a working server on
`:8080` with data in `./frame-data`.

| Key | Default | Meaning |
|---|---|---|
| `FRAME_DATA_DIR` | `./frame-data` | SQLite DB (`frame.db`), local media (`media/`), generated secrets (`secrets.json`, mode 0600). Back this directory up. |
| `FRAME_LISTEN` | `:8080` | Plain-HTTP listen address (use behind a reverse proxy / Cloudflare Tunnel). Ignored when `FRAME_DOMAIN` is set. |
| `FRAME_DOMAIN` | — | If set (e.g. `frame.example.com`), serve HTTPS on `:443` with automatic Let's Encrypt certificates and redirect `:80` → HTTPS. |
| `FRAME_ACME_EMAIL` | — | Optional contact email for Let's Encrypt. |
| `FRAME_PUBLIC_URL` | `https://$FRAME_DOMAIN` | Canonical external URL(s), comma-separated if the server is reachable under several names (first one is used in invite links). **Required for Sign in with Apple / Google**: ID-token nonces are bound to these hosts (see API.md); without it those methods are disabled. |
| `FRAME_NAME` | `Frame` | Server display name shown in the app. |
| `FRAME_REGISTRATION` | `invite` | `invite` (default: new accounts need an invite — from the admin panel, or a member's link if `FRAME_USER_INVITES`), `open` (anyone who knows the address), or `closed` (only admins create accounts). |
| `FRAME_USER_INVITES` | `true` | On invite-only servers, whether members' friend-invite links can also register new people. |
| `FRAME_ADMIN` | `on` | Web admin panel at `/admin`: `on`, `local` (only from loopback / `FRAME_TRUSTED_PROXIES` networks; a reverse proxy or tunnel on the same host must be listed in `FRAME_TRUSTED_PROXIES`, or its requests are refused), or `off`. |
| `FRAME_ADMIN_LISTEN` | — | Optional separate listen address for the admin panel (e.g. `127.0.0.1:8081`, reach it over SSH tunnel); when set, `/admin` is not served on the main listener. |
| `FRAME_AUTH_METHODS` | `email,phone,apple,google` | Enabled sign-in methods. |
| `FRAME_APPLE_AUDIENCES` | `com.crowtech.frame,com.crowtech.frame.signin` | Accepted `aud` for Sign in with Apple (iOS bundle id, Android Services ID). |
| `FRAME_GOOGLE_CLIENT_IDS` | official Frame app client ids (set at release) | Accepted `aud` for Google ID tokens. |
| `FRAME_STORAGE` | `local` | `local` or `s3`. |
| `FRAME_S3_ENDPOINT` | — | e.g. `https://<accountid>.r2.cloudflarestorage.com` |
| `FRAME_S3_BUCKET` | — | Bucket name (private bucket). |
| `FRAME_S3_REGION` | `auto` | `auto` for R2. |
| `FRAME_S3_ACCESS_KEY_ID` / `FRAME_S3_SECRET_ACCESS_KEY` | — | Bucket-scoped API token credentials. |
| `FRAME_S3_PREFIX` | `frame/` | Object key prefix. |
| `FRAME_S3_ENCRYPT` | `true` | AES-256-GCM encrypt objects before upload (key in `secrets.json` — losing it loses media). |
| `FRAME_PUSH_RELAY` | `https://frame.crow.sg` | Push relay base URL; empty string disables push. |
| `FRAME_INVITE_BASE` | `https://frame.crow.sg/i` | Base of shareable invite links. |
| `FRAME_MAX_UPLOAD_MB` | `50` | Max multipart upload size. |
| `FRAME_MAX_VIDEO_SECONDS` | `15` | Advertised max video length (clients enforce; server checks `duration_ms` meta). |
| `FRAME_MAX_FRIENDS` | `0` | 0 = unlimited. |
| `FRAME_TRUSTED_PROXIES` | — | Comma-separated CIDRs of reverse proxies whose `X-Forwarded-For` is trusted (e.g. the Caddy container network). Adding the word `cloudflare` additionally trusts `CF-Connecting-IP`, but **only** from loopback and the listed CIDRs (e.g. `cloudflare,127.0.0.1/32` for a cloudflared on the same host, or the cloudflared container's network) — never from arbitrary private addresses. |
| `FRAME_SMTP_URL` | — | e.g. `smtps://user:pass@smtp.example.com:465`; enables email password reset. |
| `FRAME_SMTP_FROM` | — | From address for reset emails. |
| `FRAME_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (JSON logs to stderr, never logs tokens, passwords or message text). |

## CLI
```
frame-server [serve] [--config FILE]       run the server (default command)
frame-server dev                           dev server on :8787, ./server/.devdata, seeded demo users
frame-server admin setup-link                 print a one-time link to create the first admin (also printed at first start)
frame-server admin user list
frame-server admin user create --email E | --phone P [--name N] [--username U] [--admin]   (prints a one-time password)
frame-server admin user promote <username>      make an existing user an admin of the web panel
frame-server admin user demote <username>       (refuses to remove the last admin)
frame-server admin user reset-2fa <username>    turn off an admin's two-factor authentication
frame-server admin user reset-password <username|email|phone>                     (prints a one-time password)
frame-server admin user unlink <username|email|phone> apple|google                  (removes a linked sign-in, signs out all devices)
frame-server admin user delete <username>
frame-server admin invite create <username>                                     (prints that user's friend-invite link)
frame-server admin invite server [--uses N] [--days D] [--note TEXT]           (prints a server invite link; --days 0 = never expires)
frame-server backup <file.db>                online, consistent SQLite backup (VACUUM INTO)
frame-server version
```

**Dev mode differs on purpose:** `frame-server dev` forces `FRAME_REGISTRATION=open`
(so app UI tests can register freely), turns push off, and seeds `alice` as an
admin of the web panel (`http://localhost:8787/admin/`, password
`password123`) instead of printing a setup link. Real environment variables
still override the dev defaults.

Settings that are **not** set in the environment/config file can be changed in
the admin panel (stored in the database). Settings set in the environment are
shown in the panel as locked ("set in frame.conf").
