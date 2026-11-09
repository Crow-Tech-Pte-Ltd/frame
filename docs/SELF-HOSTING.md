# Self-hosting Frame

Frame is a photo-sharing app for you and your friends. The photos live on a
**server you control**: a €4/month virtual server, a Raspberry Pi at home, or
an old laptop. This guide walks you through it step by step. You don't need to
be a sysadmin. If you can copy commands into a terminal and edit a text file,
you can run Frame.

> **What you get:** one small program (`frame-server`), one data folder, and
> no outside services needed. It uses about 30 MB of RAM, so a Raspberry Pi is
> plenty for a group of friends.

## Contents

1. [Pick a recipe](#1-pick-a-recipe)
2. [Recipe A: a cheap VPS with your own domain (built-in HTTPS)](#recipe-a--a-cheap-vps-with-your-own-domain)
3. [Recipe B: a home computer or Raspberry Pi with Cloudflare Tunnel](#recipe-b--home-computer-or-raspberry-pi-with-cloudflare-tunnel)
4. [Recipe C: Docker Compose](#recipe-c--docker-compose)
5. [Recipe D: behind Caddy (or another reverse proxy)](#recipe-d--behind-caddy-or-another-reverse-proxy)
6. [Where photos are stored: local disk or Cloudflare R2](#6-where-photos-are-stored-local-disk-or-cloudflare-r2)
7. [Who can join: registration, invites, friends](#7-who-can-join-registration-invites-friends)
8. [Admin panel and commands](#8-admin-panel-and-commands)
9. [Backups and restoring](#9-backups-and-restoring)
10. [Upgrading](#10-upgrading)
11. [Push notifications and the relay](#11-push-notifications-and-the-relay)
12. [How much disk, bandwidth and money?](#12-how-much-disk-bandwidth-and-money)
13. [Troubleshooting](#13-troubleshooting)
14. [Security checklist](#14-security-checklist)

The full list of settings is in [CONFIG.md](CONFIG.md), and the annotated
example file is [`deploy/frame.conf.example`](../deploy/frame.conf.example).

---

## 1. Pick a recipe

| You have… | Use | HTTPS by | Cost |
|---|---|---|---|
| Nothing yet, and you want the simplest always-on setup | **A: VPS + domain** | Frame itself (Let's Encrypt) | about €4–6/month + domain (~€10/year) |
| A Raspberry Pi or home PC, and no port forwarding (or CGNAT) | **B: Cloudflare Tunnel** | Cloudflare | electricity + domain |
| A server where you already use Docker | **C: Docker Compose** | Frame, a tunnel or a proxy | — |
| A server that already runs a web server, or you like Caddy | **D: Caddy** | Caddy | — |

Whichever you pick, in the end you have an address like
`frame.example.com` that you and your friends type into the Frame app.

> **Always set `FRAME_PUBLIC_URL`** to that address, e.g.
> `FRAME_PUBLIC_URL=https://frame.example.com`. Every recipe below does this,
> for two reasons:
>
> - **Sign in with Apple and Google only work when it is set.** Each sign-in
>   is bound to your server's name. That stops another Frame server, run by
>   someone else, from reusing a login token you gave it to sign in as you on
>   *your* server.
> - Frame uses it in invite links.
>
> If people reach your server under several names (say a public domain and a
> LAN name), list them all, separated by commas, with the main one first.

**What is a domain, and do I need one?** A domain is a name like
`example.com` that you rent from a registrar such as Cloudflare, Namecheap,
INWX or OVH, for about €10 a year. The apps need HTTPS, and HTTPS needs a name.
If you already own a domain, a subdomain such as `frame.yourname.com` costs
nothing extra.

> **Before you begin:** commands that start with `sudo` run as the
> administrator. **Never** pipe a script from the internet straight into a
> shell. Download it, read it, then run it. This guide always does it that way.

---

## Recipe A — a cheap VPS with your own domain

This is the simplest always-on setup. Frame gets and renews its own HTTPS
certificate.

**1. Rent a small server.** Any provider works. Two cheap European options are
Hetzner Cloud (CX/CAX line) and netcup VPS, at around €4–6 a month with
2–4 GB RAM, 40 GB+ disk and terabytes of traffic. Prices change, so check
their sites. Choose **Debian 13** or **Ubuntu 24.04 LTS**, and add your SSH
key when you create it.

**2. Point your domain at it.** At your DNS provider, create:

| Type | Name | Value |
|---|---|---|
| `A` | `frame` (or `@` for the bare domain) | the server's IPv4 address |
| `AAAA` | `frame` | the server's IPv6 address (optional) |

If your DNS is on Cloudflare, set the record to **DNS only** (grey cloud).
Check that it works: `dig +short frame.example.com` should print your
server's IP. This can take a few minutes.

**3. Log in and update the server.**
```sh
ssh root@frame.example.com        # or the user your provider created
sudo apt update && sudo apt upgrade -y
```

**4. Open the firewall** for SSH and web traffic, but nothing else:
```sh
sudo apt install -y ufw
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp
sudo ufw enable
```
Some providers, such as Hetzner, also have a firewall in their control panel.
If you use it, allow TCP 22, 80 and 443 there too.

**5. Download the installer, read it, then run it.** It downloads the
`frame-server` release for your CPU and **checks its SHA-256 checksum against
the release's `SHA256SUMS` file before installing anything**. It also sets up a
locked-down systemd service.
```sh
curl -fsSLO https://github.com/Crow-Tech-Pte-Ltd/frame/releases/latest/download/install.sh
less install.sh                   # read it (press q to quit)
sudo sh install.sh
```

**6. Configure it.** The installer created `/etc/frame/frame.conf`, in which
every line is commented out. Open it with `sudo nano /etc/frame/frame.conf`,
then remove the `#` and fill in at least:
```ini
FRAME_DOMAIN=frame.example.com
FRAME_PUBLIC_URL=https://frame.example.com
FRAME_ACME_EMAIL=you@example.com
FRAME_NAME="Our Frame"
```
Save with Ctrl+O, Enter, then exit with Ctrl+X. New servers are
**invite-only** by default (`FRAME_REGISTRATION=invite`), so strangers who
find the address can't sign up. Leave that line out unless you want to pin
it: anything set in `frame.conf` is locked in the admin panel. Leave `FRAME_TRUSTED_PROXIES`
unset here: phones connect to Frame directly, with no proxy in between.

**7. Allow HTTPS ports and start the server.**
```sh
sudo sh install.sh --reconfigure     # lets Frame bind ports 80/443 because FRAME_DOMAIN is set
sudo systemctl start frame
systemctl status frame               # should say "active (running)"
journalctl -u frame -f               # live logs; Ctrl+C to stop watching
```
The first HTTPS request triggers the certificate, which takes a few seconds.
Then open `https://frame.example.com/healthz` in a browser. It should say `ok`.

**8. Create your admin account.** On its first start, the server prints a
one-time link to its log:
```sh
sudo journalctl -u frame | grep "admin account"
# Create your admin account: https://frame.example.com/admin/setup#t=…
```
Open it in your browser and pick your name, a username, your e-mail (or
phone) and a password. That's your normal Frame account **and** the admin of
the web panel at `https://frame.example.com/admin/`. The link works once and
expires after 24 hours; `sudo frame-admin setup-link` prints a fresh one.

Then:

- In the Frame app, enter `frame.example.com` and sign in with the same
  details.
- In the panel, open **Security** and turn on two-factor authentication.
- Open **Invites → New invite** to get a link (and QR code) for each friend,
  or invite friends from the app (see
  [section 7](#7-who-can-join-registration-invites-friends)).

Prefer the command line? `sudo frame-admin user create --email you@example.com --name "Your Name" --admin`
prints a one-time password instead.

---

## Recipe B — home computer or Raspberry Pi with Cloudflare Tunnel

Run Frame at home with **no open ports and no router configuration**. This
also works behind CGNAT. Cloudflare's free plan carries the traffic through
an outbound-only tunnel.

**You need:**

- A Raspberry Pi 3B+/4/5/Zero 2 W, or any Linux PC, running a **64-bit** OS
  such as Raspberry Pi OS Lite (64-bit), Debian or Ubuntu.
- A domain whose DNS is managed by Cloudflare. Adding a domain to Cloudflare's
  free plan is free: you switch its nameservers at your registrar.
- Preferably an SSD or USB stick for the data rather than the SD card, which
  wears out. You can also use R2 for media
  ([section 6](#6-where-photos-are-stored-local-disk-or-cloudflare-r2)).

**1. Install Frame.** Follow steps 3 and 5 of Recipe A (update, then
download, read and run `install.sh`). Skip the firewall step: nothing needs
to be opened.

**2. Configure it** with `sudo nano /etc/frame/frame.conf`:
```ini
FRAME_LISTEN=127.0.0.1:8080
FRAME_PUBLIC_URL=https://frame.example.com
FRAME_TRUSTED_PROXIES=cloudflare,127.0.0.1/32
FRAME_NAME="Our Frame"
```
What these lines do:

- `FRAME_LISTEN=127.0.0.1:8080` means only programs on this machine, such as
  the tunnel, can reach the server.
- `FRAME_TRUSTED_PROXIES=cloudflare,127.0.0.1/32` makes Frame trust the
  visitor IP that Cloudflare passes along (the `CF-Connecting-IP` header), but
  only when the connection comes from `cloudflared` on this machine. Rate
  limits then apply per visitor rather than to everyone at once, and nobody
  else can fake the header.
- `FRAME_PUBLIC_URL` enables Sign in with Apple/Google (see
  [section 1](#1-pick-a-recipe)).

Don't set `FRAME_DOMAIN` in this recipe. Then start it:
```sh
sudo systemctl start frame
curl http://127.0.0.1:8080/healthz   # → ok
```

**3. Create the tunnel** in the Cloudflare dashboard:
1. Go to **Zero Trust → Networks → Tunnels → Create a tunnel**, choose
   **Cloudflared**, and name it `frame`.
2. Choose your OS and architecture (Debian, arm64 for a Pi). Cloudflare shows
   two commands: one installs `cloudflared` from Cloudflare's package
   repository, and one is `sudo cloudflared service install <token>`. Run
   both on your machine. The token is a secret, so don't share it.
3. Under **Public Hostname**, set subdomain `frame`, domain `example.com`,
   service type **HTTP**, URL `localhost:8080`. Save.

**4. Test it.** Opening `https://frame.example.com/healthz` from your phone
(on mobile data, not Wi-Fi) should show `ok`. Then create your admin account
with the setup link, as in step 8 of Recipe A.

Notes:

- Don't put **Cloudflare Access** (a login page) in front of this hostname.
  The apps can't get through it.
- Cloudflare's free plan limits uploads to 100 MB per request. Frame's
  default limit is 50 MB, so that's fine.
- Cloudflare terminates TLS, so it can technically see the traffic. See
  [SECURITY.md](SECURITY.md). If that bothers you, use Recipe A.

---

## Recipe C — Docker Compose

If you already use Docker, or don't want to touch systemd:

**1. Install Docker Engine.** Use your distribution's packages or
[Docker's official instructions](https://docs.docker.com/engine/install/),
which add their apt repository.

**2. Get the repository and create the config.**
```sh
git clone https://github.com/Crow-Tech-Pte-Ltd/frame.git
cd frame/deploy
cp frame.conf.example frame.conf
nano frame.conf          # FRAME_PUBLIC_URL, FRAME_NAME, plus the option below
```

**3. Choose how it is reached.** Pick one; each is explained in
[`docker-compose.yml`](../deploy/docker-compose.yml):

In every case, set `FRAME_PUBLIC_URL=https://frame.example.com` in
`frame.conf`. The compose file pins its Docker network to `172.31.251.0/24`,
so the proxy settings below can name it.

- **Built-in HTTPS:** set `FRAME_DOMAIN=frame.example.com` in `frame.conf`,
  and in `docker-compose.yml` replace the `127.0.0.1:8080:8080` port line with
  `80:80` and `443:443`. DNS and firewall are as in Recipe A. Leave
  `FRAME_TRUSTED_PROXIES` unset.
- **Cloudflare Tunnel:**
  1. Remove the `ports:` block.
  2. Uncomment the `cloudflared` service **and** the line marked (b),
     `FRAME_TRUSTED_PROXIES: "cloudflare,172.31.251.0/24"`. Frame then
     accepts the visitor IP only from the tunnel container.
  3. Put `CLOUDFLARE_TUNNEL_TOKEN=<token>` in `deploy/.env`.
  4. In the Cloudflare dashboard, set the public hostname's service to
     `http://frame:8080`.
- **Your own proxy on the host** (nginx, Caddy, … → `127.0.0.1:8080`): keep
  the port line and uncomment the line marked (d),
  `FRAME_TRUSTED_PROXIES: "172.31.251.1/32"`. Docker delivers those
  connections from the network's gateway address.
- **Caddy in Docker:** use Recipe D.

**4. Start it.** This pulls the official image
`ghcr.io/crow-tech-pte-ltd/frame-server` (for amd64 and arm64, so it also
runs on a 64-bit Raspberry Pi):
```sh
docker compose up -d
docker compose logs -f frame
```

**Prefer to build it yourself?** The image is built from
[`deploy/Dockerfile`](../deploy/Dockerfile) in this repository. Add the
[`docker-compose.build.yml`](../deploy/docker-compose.build.yml) override:
```sh
echo "COMPOSE_FILE=docker-compose.yml:docker-compose.build.yml" >> .env
docker compose up -d --build
```
With that line in `deploy/.env`, every other `docker compose` command in this
guide uses your own build too.

The container runs as an unprivileged user on a read-only filesystem, with no
Linux capabilities. Everything Frame stores lives in the Docker volume
`frame_frame-data`.

Create your admin account with the one-time link from the logs (see step 8
of Recipe A):
```sh
docker compose logs frame | grep "admin account"
docker compose exec frame frame-server admin setup-link     # a fresh link, if needed
```

Admin commands in Docker:
```sh
docker compose exec frame frame-server admin user list
docker compose exec frame frame-server admin user create --email you@example.com --name "You"
```

---

## Recipe D — behind Caddy (or another reverse proxy)

[Caddy](https://caddyserver.com) is a web server that handles HTTPS
automatically. Use it if the machine already serves other websites.

### With Docker

```sh
cd frame/deploy
cp frame.conf.example frame.conf             # edit; do NOT set FRAME_DOMAIN
echo "FRAME_HOSTNAME=frame.example.com" > .env
docker compose -f docker-compose.caddy.yml up -d
```
To build the image yourself, add `-f docker-compose.build.yml` after
`-f docker-compose.caddy.yml` (see Recipe C).
Point DNS at the machine and open ports 80 and 443, as in Recipe A.
[`docker-compose.caddy.yml`](../deploy/docker-compose.caddy.yml) sets two
things for you from `FRAME_HOSTNAME`:

- `FRAME_PUBLIC_URL=https://<FRAME_HOSTNAME>`, which is needed for Apple and
  Google sign-in.
- `FRAME_TRUSTED_PROXIES`, set to the Compose network Caddy lives on
  (`172.31.250.0/24`, pinned in the file), so Frame sees each visitor's IP
  instead of Caddy's.

Caddy health-checks Frame through `/healthz`.

### With Caddy installed on the host

Install Frame as in Recipe A (steps 1–6), but in `/etc/frame/frame.conf` set
the lines below, and not `FRAME_DOMAIN`, because Caddy handles HTTPS:
```ini
FRAME_LISTEN=127.0.0.1:8080
FRAME_PUBLIC_URL=https://frame.example.com
FRAME_TRUSTED_PROXIES=127.0.0.1/32,::1/128
```
`FRAME_PUBLIC_URL` enables Apple/Google sign-in. `FRAME_TRUSTED_PROXIES`
tells Frame that the `X-Forwarded-For` header from Caddy on this machine
carries the real visitor IP.
Install Caddy from [its official packages](https://caddyserver.com/docs/install),
then add this to `/etc/caddy/Caddyfile` (the full commented version is in
[`deploy/Caddyfile`](../deploy/Caddyfile)):
```caddy
frame.example.com {
	request_body {
		max_size 64MB
	}
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
		health_uri /healthz
	}
}
```
Then run `sudo systemctl reload caddy` and `sudo systemctl start frame`.

**Using nginx or another proxy?** You need four things:

- Pass `Host` and `X-Forwarded-For`.
- Allow request bodies of about 64 MB (nginx: `client_max_body_size 64m;`).
- Don't buffer `/api/v1/events`, which is a Server-Sent Events stream (nginx:
  `proxy_buffering off;` and `proxy_read_timeout 1h;`).
- Add the proxy's IP to `FRAME_TRUSTED_PROXIES`.

---

## 6. Where photos are stored: local disk or Cloudflare R2

**Local disk (default).** Photos and videos are stored as files in the data
directory: `/var/lib/frame/media` with systemd, or the `frame-data` volume
with Docker. This is the simplest option, and backups are just copies of the
folder.

**Cloudflare R2 (or any S3-compatible bucket).** This is useful when your
disk is small (a Pi's SD card, a tiny VPS) or you want media off the machine.
R2 has **10 GB of free storage and free egress**, so viewing photos costs
nothing. Frame **encrypts every file before uploading it** (AES-256-GCM), so
Cloudflare only ever stores ciphertext.

Step by step:

1. In the Cloudflare dashboard, open **R2 Object Storage**. The first time,
   Cloudflare asks for a payment method. You aren't charged within the free
   tier.
2. **Create a bucket**, for example `frame-media`, with automatic location.
   Leave it **private**: don't enable public access or a public `r2.dev` URL.
   Frame serves the media itself.
3. **Create an API token** under **R2 → Manage API tokens → Create API token**:
   - Permissions: **Object Read & Write**.
   - Scope: **Apply to specific buckets only**, then pick `frame-media`.
   - Create it, then copy the **Access Key ID**, the **Secret Access Key**
     (shown only once) and the **S3 endpoint**
     (`https://<account-id>.r2.cloudflarestorage.com`).
4. **Configure Frame** in `frame.conf`:
   ```ini
   FRAME_STORAGE=s3
   FRAME_S3_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
   FRAME_S3_BUCKET=frame-media
   FRAME_S3_REGION=auto
   FRAME_S3_ACCESS_KEY_ID=<access key id>
   FRAME_S3_SECRET_ACCESS_KEY='<secret access key>'
   # FRAME_S3_ENCRYPT=true   (the default; keep it)
   ```
   Restart (`sudo systemctl restart frame`, or `docker compose up -d`), post a
   photo, and check that objects appear under `frame/` in the bucket.
5. **Back up the encryption key.** Frame created `secrets.json` in its data
   directory. It holds the key that encrypts your R2 objects.
   **If you lose `secrets.json`, every photo in the bucket is unreadable
   forever.** Keep a copy somewhere safe, such as a password manager or an
   offline USB stick (see [section 9](#9-backups-and-restoring)).

Decide on storage **before** your friends start posting. Switching an existing
server between local and R2 is not automatic.

---

## 7. Who can join: registration, invites, friends

New servers are **invite-only**. Change this in the admin panel under
**Settings → Who can join**, or pin it with `FRAME_REGISTRATION` in
`frame.conf`:

| Mode | Who can create an account | Good for |
|---|---|---|
| `invite` (default) | Only people with an invite: a link from the admin panel, or — when **Members can invite** is on (`FRAME_USER_INVITES`, default on) — a link a member shares from the app | **Most servers.** You and your friends invite friends. |
| `open` | Anyone who knows your server address | Quick tests. Not recommended for a public server. |
| `closed` | Nobody. Admins create accounts in the panel (**Users → New user**) or with `frame-admin user create` | Family servers with a fixed list of people. |

Signing in to an existing account never needs an invite.

**Server invites (from the admin panel).** **Invites → New invite** makes a
link that lets someone create an account — by default once, within 7 days.
Add a note ("for Mum") to remember who it's for, copy the link or show the QR
code, and see who joined with it. It doesn't make them anyone's friend; they
add friends in the app. Same from the command line:
`sudo frame-admin invite server --uses 1 --days 7 --note "for Mum"`.

**Members' invite links (from the app).** In the app, open **Friends →
Invite** and share the link, which looks like
`https://frame.crow.sg/i#s=…&c=…`. When your friend opens it:

- If they have Frame, the link opens it with your server filled in, and after
  sign-up you're **already friends**.
- If not, the page shows App Store and Google Play buttons and a "copy server
  address" button.
- Your server address and the code travel in the part after `#`, which
  browsers never send to `frame.crow.sg`.

These links are valid for 30 days and can be used several times. Turn off
**Members can invite** if only you should decide who joins; members' links
then still work for adding friends who are already on the server. You can
also make one on the server: `sudo frame-admin invite create <username>`.

**Revoking an invite:**

- Shared a link with the wrong person? In the app, use **Settings → Reset
  invite link**. This revokes all your open invites at once and gives you a
  new link. Revoked or expired links stop working immediately.
- In the admin panel, **Invites** lists every server invite and every
  member's open link, each with a **Revoke** button.
- Someone you previously **unfriended or blocked** can't use an old or
  forwarded invite to get back in. For them the link only creates a normal
  friend request that you must accept. A blocked person gets nothing.

**Friends on the same server** can also find each other by username search,
or by contacts if they allow it. There's no friend limit unless you set
`FRAME_MAX_FRIENDS`.

> Everyone on the same server can use Frame together. Friends on **different**
> servers can't see each other's photos. Pick one server per group of friends.

---

## 8. Admin panel and commands

**The web panel** lives at `https://your-domain/admin/` and is described in
detail in [ADMIN.md](ADMIN.md): users (reset passwords, suspend, make admin,
delete), member reports, invites, settings, two-factor authentication, an
audit log and backups. It never shows anyone's photos or messages, except a
single item a member reports to you. If you'd rather not
expose it to the internet at all, set `FRAME_ADMIN=local` or serve it on a
separate local address with `FRAME_ADMIN_LISTEN=127.0.0.1:8081` and reach it
through an SSH tunnel ([ADMIN.md §4](ADMIN.md#4-where-the-panel-is-reachable)).

**Commands.** With the systemd install, always use `sudo frame-admin …`. It runs the
command as the service's own user. Running `frame-server` directly as root
would create database files the service can't write.

```sh
sudo frame-admin setup-link                                              # one-time link to create the first admin
sudo frame-admin user list
sudo frame-admin user create --email alice@example.com --name Alice     # prints a one-time password
sudo frame-admin user create --phone +34600111222 --name Bob
sudo frame-admin user create --email you@example.com --name You --admin # an admin for the web panel
sudo frame-admin user promote alice                                      # make an existing user an admin
sudo frame-admin user demote alice                                       # (never the last admin)
sudo frame-admin user reset-2fa alice                                    # admin lost their authenticator
sudo frame-admin user reset-password alice                               # prints a one-time password
sudo frame-admin user delete alice                                       # irreversible
sudo frame-admin invite server --uses 1 --days 7 --note "for Mum"        # server invite link
sudo frame-admin invite create alice                                     # alice's friend-invite link
sudo frame-admin backup /root/frame-backup.db
sudo frame-admin version
```
With Docker, use `docker compose exec frame frame-server admin …` instead.

Password resets by e-mail only work if you configure `FRAME_SMTP_URL`.
Otherwise the app tells people to ask you, and you use **Reset password** in
the panel (or `reset-password`).

Other useful commands:
```sh
systemctl status frame             # running?
journalctl -u frame -n 100         # last 100 log lines
sudo systemctl restart frame       # after editing frame.conf
```

---

## 9. Backups and restoring

What to back up:

| What | Where (systemd) | Why |
|---|---|---|
| Database | made with `frame-admin backup` | accounts, friendships, messages, moments |
| `secrets.json` | `/var/lib/frame/secrets.json` | signing keys, and **the R2 encryption key** |
| Media (local storage only) | `/var/lib/frame/media/` | the photos and videos |

**systemd:**
```sh
sudo frame-admin backup /root/frame-$(date +%F).db       # consistent, safe while running
sudo tar -C /var/lib/frame -czf /root/frame-files-$(date +%F).tgz secrets.json media
```
Copy both files off the server, for example with `scp` to your computer, or
with `restic` or `rclone` to another provider. Run this nightly with cron
(`sudo crontab -e`):
```
15 3 * * * /usr/local/bin/frame-admin backup /root/frame-$(date +\%F).db && tar -C /var/lib/frame -czf /root/frame-files-$(date +\%F).tgz secrets.json media
```
Old backups pile up, so delete them now and then, or let your backup tool
rotate them.

**Docker:**
```sh
docker compose exec frame frame-server backup /data/frame-backup.db
docker compose cp frame:/data/frame-backup.db ./frame-$(date +%F).db
docker run --rm -v frame_frame-data:/data:ro -v "$PWD":/backup alpine \
  tar -C /data -czf /backup/frame-files-$(date +%F).tgz secrets.json media
```

**With R2**, the media is already in the bucket, so the database and
`secrets.json` are what matter. For extra safety you can mirror the bucket
elsewhere with `rclone sync`.

**Restoring** (systemd; on a new machine, run `install.sh` first):
```sh
sudo systemctl start frame && sudo systemctl stop frame   # makes sure the data dir exists
sudo tar -C /var/lib/frame -xzf frame-files-2026-11-07.tgz
sudo cp frame-2026-11-07.db /var/lib/frame/frame.db
sudo rm -f /var/lib/frame/frame.db-wal /var/lib/frame/frame.db-shm
# hand everything back to the service's user (the owner of the data dir itself):
sudo chown -R --reference=/var/lib/private/frame /var/lib/private/frame
sudo systemctl start frame
```

---

## 10. Upgrading

Back up first ([section 9](#9-backups-and-restoring)). Then:

**systemd:** download and read the installer again, then run it. It verifies
the checksum, replaces the binary and restarts the service. Your configuration
and data are untouched.
```sh
curl -fsSLO https://github.com/Crow-Tech-Pte-Ltd/frame/releases/latest/download/install.sh
less install.sh
sudo sh install.sh                     # or: sudo sh install.sh --version v1.2.3
```

**Docker:**
```sh
cd frame && git pull                      # updates the compose files
cd deploy && docker compose pull && docker compose up -d
```
If you build the image yourself (Recipe C), run `docker compose up -d --build`
after `git pull` instead.

Database migrations run automatically on start. To be told about new releases,
use **Watch → Custom → Releases** on the GitHub repository.

---

## 11. Push notifications and the relay

Apple and Google only deliver notifications that are signed with the **app
publisher's** keys, and self-hosted servers can't have those. So your server
hands notifications to a small relay run by the Frame publisher at
`https://frame.crow.sg`, which passes them on to Apple and Google.

The relay is built so it learns as little as possible:

- **The content is end-to-end encrypted.** Each phone makes its own key and
  shares it only with your server. The relay, Apple and Google only see
  ciphertext plus a generic "Frame: New activity".
- **The relay stores nothing.** It has no database and does not log tokens,
  payloads or IP addresses.
- **Your server never sees the phones' real push tokens**, only sealed
  `relay_token`s that only the relay can open.

What the relay *can* know is that some server, identified by its IP address,
asked it to deliver a notification of a certain size at a certain time.

**Push permission expires.** Each phone's sealed relay token stops working
after **60 days**. The apps fetch a fresh one every time they open and send it
to your server, so phones that are in use never notice. If someone uninstalls
the app or stops using a server, that server loses the ability to notify them
after at most 60 days. Logging out removes it immediately.

**To turn push off**, set an empty value and restart:
```ini
FRAME_PUSH_RELAY=
```
The apps still work. Widgets refresh about every 15 minutes, and the app
updates live while it's open. Notifications for new messages only arrive when
the app is opened.

The relay protocol is specified in [PUSH.md](PUSH.md). Running your own relay
only makes sense if you also build and sign the apps yourself.

---

## 12. How much disk, bandwidth and money?

Assumptions: photos are about **400 KB** and their thumbnails about
**60 KB**. A 10-second video is about **3 MB**. Each photo or video is stored
**once**, however many friends receive it.

| Scenario (per year) | Uploads/day | Storage/day | **Storage/year** |
|---|---|---|---|
| 10 friends × 5 photos/day | 50 photos | 50 × 460 KB = 23 MB | **≈ 8.4 GB** |
| 10 friends × (4 photos + 1 ten-second video)/day | 40 photos + 10 videos | 18.4 MB + 30.6 MB = 49 MB | **≈ 18 GB** |
| 4 people × 3 photos/day (family) | 12 photos | 5.5 MB | **≈ 2 GB** |

The database (messages, reactions, accounts) stays tiny in comparison: a few
hundred MB after years.

**Bandwidth.** In the photos-only case, if every moment reaches 9 friends who
each load the thumbnail and the full photo, that's about 200 MB a day, or
roughly **6 GB a month** of downloads. With videos it's about 13 GB a month.
Cheap VPSs include terabytes, and R2 egress is free.

**What it costs:**

| Setup | Monthly |
|---|---|
| VPS (Recipe A/C/D) with local storage | about €4–6. A 40 GB disk lasts the photos-only group ~4 years. |
| Raspberry Pi at home (Recipe B) | electricity (a Pi 4 draws about 3–5 W, roughly €1/month) |
| + Cloudflare R2 | **€0** up to 10 GB. Beyond that, about $0.015 per GB-month (≈ $0.15/month for 20 GB). Upload/download operations stay far below the free tier. |
| + a domain | about €10/year |

**Hardware:** 1 CPU core and 512 MB–1 GB RAM are enough. Frame itself uses
roughly 30–60 MB. Uploads are written as-is, and the server never re-encodes
images or video, so even a Pi stays idle most of the time.

---

## 13. Troubleshooting

**The app says it can't connect.**

1. On your phone's browser, open `https://your-domain/api/v1/server`. You
   should see JSON with your server name.
2. If the page doesn't load:
   - DNS: does `dig +short your-domain` show your server's IP?
   - Firewall: `sudo ufw status` should allow 80 and 443. Check the
     provider's firewall too.
   - Service: run `systemctl status frame` and `journalctl -u frame -n 50`.
   - Tunnel: is `cloudflared` running (`systemctl status cloudflared`), and
     is the public hostname set to `localhost:8080`?
3. Type the address without `https://` and without a trailing slash, such as
   `frame.example.com`. The app adds the rest.

**A friend can't sign up ("invite required" or "invite invalid").** The
server is invite-only. Send them a link from **Invites → New invite** in the
admin panel, or have a member share theirs from the app. "Invalid" means the
link was revoked, expired or used up (server invites are single-use by
default), or that **Members can invite** is off and they used a member's
link. Check **Settings** and **Invites** in the panel.

**Lost the admin setup link, or locked out of the panel.** Run
`sudo frame-admin setup-link` while no admin exists. Otherwise:
`sudo frame-admin user reset-password <you>` for a forgotten password,
`sudo frame-admin user reset-2fa <you>` for a lost authenticator, or
`sudo frame-admin user promote <you>` to make an existing account an admin.

**HTTPS or certificate errors.**

- With `FRAME_DOMAIN`, Let's Encrypt must be able to reach ports **80 and
  443** on your server directly. If the DNS record is proxied by Cloudflare
  (orange cloud), switch it to *DNS only*, or use Recipe B instead.
- Did you run `sudo sh install.sh --reconfigure` after setting
  `FRAME_DOMAIN`? Without it, Frame can't bind ports 80/443. The log shows
  "permission denied".
- Let's Encrypt rate limits failed attempts. If you retried many times, wait
  an hour.
- The apps reject self-signed certificates. Use a real domain.

**Testing on your own computer, an emulator or the local network.**

- Run `frame-server` locally, or `go run ./cmd/frame-server dev` from a
  source checkout, which starts on port 8787.
- **Android emulator:** use `http://10.0.2.2:8080` (or `:8787` in dev mode).
  Inside the emulator, `10.0.2.2` means "the computer running the emulator",
  and `localhost` would be the emulator itself.
- **iOS simulator:** `http://localhost:8080` (or `:8787`) works.
- **A real phone on the same Wi-Fi:** use your computer's LAN address, such as
  `http://192.168.1.20:8080`. The firewall must allow port 8080, and
  `FRAME_LISTEN` must not be `127.0.0.1`.
- The apps accept plain `http://` **only** for localhost and private LAN
  addresses. Anything on the internet needs HTTPS.

**Uploads fail or videos won't send.** A proxy in front is probably limiting
the request size. Caddy: `request_body max_size 64MB`. nginx:
`client_max_body_size 64m`. Cloudflare free plan: 100 MB maximum. Also check
`FRAME_MAX_UPLOAD_MB`.

**Live updates (new messages) are slow behind a proxy.** The proxy is
buffering `/api/v1/events`. Disable buffering for that path (see Recipe D).

**Everyone gets "too many requests", or rate limits hit the wrong people.**
Frame sees your proxy's IP instead of the visitors'. Set
`FRAME_TRUSTED_PROXIES` to match your setup:

| Setup | Value |
|---|---|
| cloudflared on the same host | `cloudflare,127.0.0.1/32` |
| Caddy/nginx on the same host | `127.0.0.1/32,::1/128` |
| Docker Caddy variant | preset in the compose file |
| Docker tunnel or host proxy | the (b) or (d) line in `docker-compose.yml` |

`cloudflare` on its own trusts nobody: it must be paired with the address the
tunnel connects from.

**The Sign in with Apple / Google buttons are missing in the app.** Set
`FRAME_PUBLIC_URL` to the exact address people type into the app, then
restart. If people use more than one address, list them all, comma-separated.

**Notifications don't arrive.**

- Check that `FRAME_PUSH_RELAY` isn't empty.
- Check that the server can reach the internet:
  `curl -sI https://frame.crow.sg/healthz`.
- On the phone, allow notifications for Frame. On Android, also turn off
  battery optimisation for Frame. On iOS, check Focus modes.
- Look for `push failed` in the logs.

**"Permission denied" or "readonly database" after running admin commands.**
You probably ran `frame-server` directly as root, which left root-owned files
in the data directory. Give them back to the service and restart:
`sudo chown -R --reference=/var/lib/private/frame /var/lib/private/frame && sudo systemctl restart frame`.
From then on, use `sudo frame-admin …`.

---

## 14. Security checklist

- [ ] HTTPS works (`https://your-domain/healthz` shows a valid lock).
- [ ] `FRAME_PUBLIC_URL` is set to your address(es).
- [ ] `FRAME_TRUSTED_PROXIES` matches your setup: unset with built-in HTTPS,
      only your proxy's address otherwise.
- [ ] Registration is `invite` (the default) or `closed`, unless you really
      want an open server.
- [ ] You created your admin account with the setup link, and turned on
      two-factor authentication in the panel (**Security**).
- [ ] Consider `FRAME_ADMIN=local` or `FRAME_ADMIN_LISTEN` + an SSH tunnel if
      you don't need the admin panel on the internet.
- [ ] Only ports 22, 80 and 443 are open (or none, with a tunnel). Log in
      with SSH keys, not passwords.
- [ ] Automatic OS security updates are on
      (`sudo apt install unattended-upgrades`).
- [ ] Nightly backups of the database **and `secrets.json`** are copied off
      the server, and you've tried a restore once.
- [ ] `/etc/frame/frame.conf` (or `deploy/frame.conf` with Docker:
      `chmod 600 frame.conf`) stays private and is never committed or
      shared. It can contain R2 and SMTP credentials.
- [ ] With `FRAME_ADMIN=local`, a reverse proxy or tunnel on the same host
      is listed in `FRAME_TRUSTED_PROXIES` (otherwise the panel refuses the
      requests it forwards).
- [ ] You upgrade when a release is announced.

Read [SECURITY.md](SECURITY.md) to understand what Frame protects against, and
what it deliberately doesn't. In short: **whoever runs the server can see the
photos on it**. Only join servers run by people you trust.
