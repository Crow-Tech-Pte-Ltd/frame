#!/bin/sh
# Frame server installer for Linux with systemd (Debian, Ubuntu, Raspberry Pi
# OS, Fedora, …). Please read this script before running it:
#
#   curl -fsSLO https://github.com/Crow-Tech-Pte-Ltd/frame/releases/latest/download/install.sh
#   less install.sh
#   sudo sh install.sh
#
# What it does (and nothing else):
#   1. picks the release binary for this CPU (amd64, arm64 or armv7);
#   2. downloads it plus SHA256SUMS from the GitHub release and VERIFIES the
#      SHA-256 checksum before installing anything;
#   3. installs /usr/local/bin/frame-server, /usr/local/bin/frame-admin and
#      /etc/systemd/system/frame.service;
#   4. creates /etc/frame/frame.conf (mode 0600) from the example on first run
#      — an existing config is never touched;
#   5. adds the CAP_NET_BIND_SERVICE drop-in only if FRAME_DOMAIN is set;
#   6. enables the service; on upgrades it restarts it if it was running.
# Re-running it is safe (idempotent). Data in /var/lib/frame is never touched.
#
# Options:
#   --version vX.Y.Z   install that release instead of the latest
#   --binary PATH      install a locally built frame-server (no download); the
#                      unit/admin/example files are taken from this script's
#                      directory (a repository checkout's deploy/)
#   --start            start the service even on first install
#   --reconfigure      only re-check FRAME_DOMAIN, reload and restart
#   --uninstall        remove binary, unit and helper (keeps config and data)
#   -h, --help
# Environment: FRAME_REPO (default Crow-Tech-Pte-Ltd/frame) for forks.
set -eu

REPO=${FRAME_REPO:-Crow-Tech-Pte-Ltd/frame}
VERSION=latest
LOCAL_BIN=
FORCE_START=0
MODE=install

BIN=/usr/local/bin/frame-server
ADMIN=/usr/local/bin/frame-admin
CONF_DIR=/etc/frame
CONF=$CONF_DIR/frame.conf
UNIT=/etc/systemd/system/frame.service
DROPIN_DIR=/etc/systemd/system/frame.service.d
DROPIN=$DROPIN_DIR/10-https.conf

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}
usage() { sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
	case $1 in
	--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION=$2; shift 2 ;;
	--version=*) VERSION=${1#*=}; shift ;;
	--binary) [ $# -ge 2 ] || die "--binary needs a path"; LOCAL_BIN=$2; shift 2 ;;
	--binary=*) LOCAL_BIN=${1#*=}; shift ;;
	--start) FORCE_START=1; shift ;;
	--reconfigure) MODE=reconfigure; shift ;;
	--uninstall) MODE=uninstall; shift ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
done

# ── Preconditions ──────────────────────────────────────────────────────────
[ "$(id -u)" -eq 0 ] || die "please run as root: sudo sh $0"
[ "$(uname -s)" = Linux ] || die "this installer supports Linux only (use Docker elsewhere)"
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] ||
	die "systemd is required (use the Docker Compose setup instead)"

# ── Helpers ────────────────────────────────────────────────────────────────

# drop-in: present only when FRAME_DOMAIN has a non-empty value.
domain_set() {
	[ -r "$CONF" ] || return 1
	grep -Eq '^[[:space:]]*(export[[:space:]]+)?FRAME_DOMAIN[[:space:]]*=[[:space:]]*["'\'']?[A-Za-z0-9]' "$CONF"
}

configure_https() {
	if domain_set; then
		mkdir -p "$DROPIN_DIR"
		cat >"$DROPIN.tmp" <<'EOF'
# Added by install.sh because FRAME_DOMAIN is set: allow binding ports 80/443.
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
EOF
		chmod 0644 "$DROPIN.tmp"
		mv -f "$DROPIN.tmp" "$DROPIN"
		say "FRAME_DOMAIN is set: allowing the service to bind ports 80 and 443"
	elif [ -e "$DROPIN" ]; then
		rm -f "$DROPIN"
		rmdir "$DROPIN_DIR" 2>/dev/null || true
		say "FRAME_DOMAIN not set: removed the port 80/443 capability"
	fi
}

restart_if_running() {
	if systemctl is-active --quiet frame.service; then
		say "Restarting frame.service"
		systemctl restart frame.service
	fi
}

# ── Uninstall / reconfigure ────────────────────────────────────────────────
if [ "$MODE" = uninstall ]; then
	systemctl disable --now frame.service 2>/dev/null || true
	rm -f "$BIN" "$ADMIN" "$UNIT" "$DROPIN"
	rmdir "$DROPIN_DIR" 2>/dev/null || true
	systemctl daemon-reload
	say "Removed Frame. Kept your configuration ($CONF_DIR) and data (/var/lib/private/frame)."
	exit 0
fi
if [ "$MODE" = reconfigure ]; then
	[ -e "$UNIT" ] || die "Frame is not installed yet"
	configure_https
	systemctl daemon-reload
	restart_if_running
	exit 0
fi

# ── Architecture ───────────────────────────────────────────────────────────
case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv7l | armv7*) ARCH=armv7 ;;
*) die "unsupported CPU architecture: $(uname -m) (supported: x86_64, aarch64, armv7l)" ;;
esac
ASSET=frame-server-linux-$ARCH

TMP=$(mktemp -d "${TMPDIR:-/tmp}/frame-install.XXXXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM

if [ -n "$LOCAL_BIN" ]; then
	# ── Local binary (built from source) ───────────────────────────────────
	[ -f "$LOCAL_BIN" ] || die "no such file: $LOCAL_BIN"
	SRC_DIR=$(cd "$(dirname "$0")" && pwd)
	for f in frame.service frame-admin frame.conf.example; do
		[ -f "$SRC_DIR/$f" ] || die "$f not found next to $0"
		cp "$SRC_DIR/$f" "$TMP/$f"
	done
	cp "$LOCAL_BIN" "$TMP/$ASSET"
	TAG="local build"
else
	# ── Download from GitHub Releases and verify ───────────────────────────
	if command -v curl >/dev/null 2>&1; then
		fetch() { curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$2" "$1"; }
		latest_url() { curl -fsSLI --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "$1"; }
	elif command -v wget >/dev/null 2>&1; then
		fetch() { wget -q --https-only -O "$2" "$1"; }
		latest_url() { wget -q --https-only -S -O /dev/null "$1" 2>&1 | awk '/^ *[Ll]ocation: /{u=$2} END{print u}'; }
	else
		die "curl or wget is required"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		sha256() { sha256sum "$1" | awk '{print $1}'; }
	elif command -v shasum >/dev/null 2>&1; then
		sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
	elif command -v openssl >/dev/null 2>&1; then
		sha256() { openssl dgst -sha256 -r "$1" | awk '{print $1}'; }
	else
		die "sha256sum (coreutils), shasum or openssl is required to verify the download"
	fi

	if [ "$VERSION" = latest ]; then
		url=$(latest_url "https://github.com/$REPO/releases/latest" | tr -d '\r') ||
			die "could not determine the latest release"
		TAG=${url##*/}
	else
		TAG=$VERSION
	fi
	echo "$TAG" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$' ||
		die "unexpected release tag '$TAG'"
	BASE=https://github.com/$REPO/releases/download/$TAG

	say "Downloading Frame $TAG for linux/$ARCH"
	for f in SHA256SUMS "$ASSET" frame.service frame-admin frame.conf.example; do
		fetch "$BASE/$f" "$TMP/$f" || die "download failed: $BASE/$f"
	done

	say "Verifying SHA-256 checksums"
	for f in "$ASSET" frame.service frame-admin frame.conf.example; do
		expected=$(awk -v f="$f" '$2 == f || $2 == "*" f { print $1 }' "$TMP/SHA256SUMS")
		[ "$(printf '%s\n' "$expected" | grep -c .)" -eq 1 ] ||
			die "$f is not listed exactly once in SHA256SUMS — refusing to install"
		printf '%s' "$expected" | grep -Eq '^[0-9a-f]{64}$' || die "malformed checksum for $f"
		actual=$(sha256 "$TMP/$f")
		[ "$actual" = "$expected" ] ||
			die "CHECKSUM MISMATCH for $f (expected $expected, got $actual) — refusing to install"
	done
	say "Checksums OK"
fi

chmod 0755 "$TMP/$ASSET"
"$TMP/$ASSET" version >/dev/null 2>&1 || die "the downloaded binary does not run on this machine"

# ── Install files ──────────────────────────────────────────────────────────
FIRST_INSTALL=0
[ -e "$UNIT" ] || FIRST_INSTALL=1

if [ -f "$BIN" ] && cmp -s "$TMP/$ASSET" "$BIN"; then
	say "frame-server is already this version"
	BIN_CHANGED=0
else
	install -m 0755 "$TMP/$ASSET" "$BIN.new"
	mv -f "$BIN.new" "$BIN"
	say "Installed $BIN ($("$BIN" version 2>/dev/null || echo "$TAG"))"
	BIN_CHANGED=1
fi

# (Separate commands, not `a && b`: set -e must stop on any failure.)
install -m 0755 "$TMP/frame-admin" "$ADMIN.new"
mv -f "$ADMIN.new" "$ADMIN"
install -d -m 0755 "$(dirname "$UNIT")"
install -m 0644 "$TMP/frame.service" "$UNIT.new"
mv -f "$UNIT.new" "$UNIT"

install -d -m 0755 "$CONF_DIR"
install -m 0644 "$TMP/frame.conf.example" "$CONF_DIR/frame.conf.example"
if [ ! -e "$CONF" ]; then
	install -m 0600 -o root -g root "$TMP/frame.conf.example" "$CONF"
	say "Created $CONF (all settings commented out = defaults)"
else
	chmod go-rwx "$CONF"
fi

configure_https
systemctl daemon-reload
systemctl enable --quiet frame.service

if [ "$FIRST_INSTALL" -eq 1 ] && [ "$FORCE_START" -eq 0 ]; then
	cat <<EOF

Frame is installed but not started yet. Next steps:
  1. Edit the configuration:     sudo nano $CONF
     (at least FRAME_DOMAIN or FRAME_PUBLIC_URL, and FRAME_NAME)
  2. If you set FRAME_DOMAIN:    sudo sh $0 --reconfigure
  3. Start it:                   sudo systemctl start frame
  4. Create your admin account:  journalctl -u frame | grep -m1 "admin/setup"
     (or: sudo frame-admin setup-link) and open that link once.
     New servers are invite-only: invite friends from <your server>/admin.
  5. Check it:                   systemctl status frame; journalctl -u frame -f
  6. Admin commands:             sudo frame-admin user list

Guide: https://github.com/$REPO/blob/main/docs/SELF-HOSTING.md
EOF
elif [ "$FORCE_START" -eq 1 ] && ! systemctl is-active --quiet frame.service; then
	say "Starting frame.service"
	systemctl start frame.service
else
	restart_if_running
	[ "$BIN_CHANGED" -eq 1 ] || say "Configuration refreshed"
fi
say "Done."
