# Frame

**Live photos from your people, on your home screen, on a server you own.**

Frame is a private photo-sharing app for you and your friends. Take a photo or
a short video (or pick one from your gallery) and it appears on your friends'
home-screen widget. They react and reply in a simple chat. No algorithm, no
ads, no paywall, no AI moderation, no friend limit, and your photos live on
**your own server**: on its local disk by default, or in any Cloudflare R2 / S3
bucket.

This repository is the **Frame server**, which anyone can run:

| Part | What it is | Docs |
|---|---|---|
| `server/` | `frame-server`: one static Go binary (SQLite, ~14 MB, ~20 MB RAM). API, realtime events, media storage, push fan-out, web admin panel, admin CLI. | [server/README.md](server/README.md) |
| `admin-ui/` | Source of the web admin panel built into `frame-server` (`/admin`). | [docs/ADMIN.md](docs/ADMIN.md) |
| `deploy/` | Installer, hardened systemd unit, Docker image, Compose files (plain, Caddy, Cloudflare Tunnel). | [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md) |
| `docs/` | API contract, configuration, push protocol, security model. | see below |

## The apps

- **iOS** (iPhone, iOS 17 or later, with home-screen and lock-screen widgets):
  on the App Store as **Frame – Photos with Friends** (store links on
  [crow.sg/apps/frame](https://crow.sg/apps/frame/)).
- **Android** (Android 10 or later, with a home-screen widget):
  [Frame on Google Play](https://play.google.com/store/apps/details?id=com.crowtech.frame).

Both apps are free, with no ads or in-app purchases. A step-by-step guide to
running your own server: [crow.sg/apps/frame/docs](https://crow.sg/apps/frame/docs/).

The apps are published by Crow Tech and connect to any Frame server: you type
your server's address when you sign up. Notifications go through a small,
content-blind push relay run by Crow Tech, because Apple and Google only accept
notifications signed with the app publisher's keys. Notification content is
end-to-end encrypted between your server and your friends' phones, and you can
turn push off entirely ([docs/PUSH.md](docs/PUSH.md)).

## Run your own server

New servers are **invite-only**: on first start the server prints a one-time
link to create your admin account, and you then invite your friends from the
admin panel or the app.

**Linux with systemd** (a €4 VPS, a Raspberry Pi, a home server). The installer
downloads the release binary for your CPU (amd64, arm64 or armv7) and verifies
its SHA-256 checksum before installing anything. Read it before you run it:

```sh
curl -fsSLO https://github.com/Crow-Tech-Pte-Ltd/frame/releases/latest/download/install.sh
less install.sh
sudo sh install.sh
```

**Docker Compose** (image `ghcr.io/crow-tech-pte-ltd/frame-server`, amd64 and arm64):

```sh
git clone https://github.com/Crow-Tech-Pte-Ltd/frame.git
cd frame/deploy
cp frame.conf.example frame.conf    # set FRAME_PUBLIC_URL and FRAME_NAME
docker compose up -d
docker compose logs frame | grep "admin account"
```

**[docs/SELF-HOSTING.md](docs/SELF-HOSTING.md)** walks through every option
step by step: built-in HTTPS on a VPS, a Raspberry Pi behind a free Cloudflare
Tunnel (no port forwarding), Docker Compose, Caddy, storage on R2, backups and
upgrades.

## Documentation

- [docs/SELF-HOSTING.md](docs/SELF-HOSTING.md): installing, configuring, backing up and upgrading a server
- [docs/CONFIG.md](docs/CONFIG.md): every setting and the admin CLI
- [docs/ADMIN.md](docs/ADMIN.md): the web admin panel
- [docs/API.md](docs/API.md): the HTTP API contract between server and apps
- [docs/PUSH.md](docs/PUSH.md): push notifications, the relay protocol and widget refresh
- [docs/SECURITY.md](docs/SECURITY.md): threat model and security controls

## Develop

You need Go 1.26 (see `server/go.mod`). The admin panel is prebuilt into
`server/internal/adminui/dist/`, so only changes to the panel need
[Bun](https://bun.sh) (see [admin-ui/README.md](admin-ui/README.md)).

```sh
cd server
go test -race ./...
go run ./cmd/frame-server dev     # http://0.0.0.0:8787, demo users alice/bob/carol, password "password123"
python3 ../scripts/smoke-test.py http://127.0.0.1:8787    # in a second terminal (needs Pillow)
```

Bug reports and pull requests are welcome. Please report security issues
privately, as described in [SECURITY.md](SECURITY.md).

## License

Copyright (C) 2026 Crow Tech Pte. Ltd.

Frame is free software: you can redistribute it and/or modify it under the
terms of the GNU Affero General Public License as published by the Free
Software Foundation, version 3. It is distributed in the hope that it will be
useful, but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See [LICENSE](LICENSE)
for the full text.

If you run a modified version of the server for other people, the AGPL
requires you to offer them the source code of your version.

The licence covers the code and documentation. It does not grant rights to the
Crow Tech name and logo (`admin-ui/src/assets/`).

The admin panel bundles the Ubuntu font (Ubuntu Font Licence 1.0) and Azeret
Mono (SIL Open Font License 1.1); see
[admin-ui/public/licenses/](admin-ui/public/licenses/README.txt).
