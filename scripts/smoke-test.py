"""End-to-end smoke test of a running frame-server dev instance. Usage: python3 scripts/smoke-test.py http://127.0.0.1:8787 (needs Pillow)."""
import io, json, sys, threading, time, uuid, urllib.request, urllib.error

BASE = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8799"
results = []


def check(name, cond, detail=""):
    results.append((name, bool(cond)))
    print(("PASS " if cond else "FAIL ") + name + (f"  [{detail}]" if detail and not cond else ""))


def req(method, path, token=None, body=None, headers=None, raw=False):
    h = dict(headers or {})
    data = None
    if body is not None and not isinstance(body, bytes):
        data = json.dumps(body).encode()
        h["Content-Type"] = "application/json"
    elif isinstance(body, bytes):
        data = body
    if token:
        h["Authorization"] = "Bearer " + token
    r = urllib.request.Request(BASE + path, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            content = resp.read()
            return resp.status, (content if raw else (json.loads(content) if content else None)), resp.headers
    except urllib.error.HTTPError as e:
        content = e.read()
        try:
            return e.code, json.loads(content), e.headers
        except Exception:
            return e.code, content, e.headers


DEV = {"name": "smoke", "platform": "android"}


def login(method, ident):
    s, b, _ = req("POST", "/api/v1/auth/login", body={"method": method, "identifier": ident, "password": "password123", "device": DEV})
    assert s == 200, (s, b)
    return b["token"], b["user"]


def tiny_jpeg(seed, size=None):
    from PIL import Image
    size = size or (1440 if seed == 1 else 512)
    out = io.BytesIO()
    Image.new("RGB", (size, size), (200, 30 * seed, 40)).save(out, "JPEG", quality=80)
    return out.getvalue()


def post_moment(token, audience="all", recipients=None, caption=None):
    boundary = "----frame" + uuid.uuid4().hex
    meta = {"client_id": str(uuid.uuid4()), "kind": "photo", "caption": caption, "audience": audience,
            "recipient_ids": recipients or [], "width": 1440, "height": 1440, "duration_ms": None,
            "local_date": time.strftime("%Y-%m-%d")}
    parts = [("meta", "application/json", json.dumps(meta).encode()),
             ("media", "image/jpeg", tiny_jpeg(1)), ("thumb", "image/jpeg", tiny_jpeg(2))]
    buf = io.BytesIO()
    for name, ctype, data in parts:
        buf.write(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"; filename=\"{name}\"\r\nContent-Type: {ctype}\r\n\r\n".encode())
        buf.write(data + b"\r\n")
    buf.write(f"--{boundary}--\r\n".encode())
    return req("POST", "/api/v1/moments", token, buf.getvalue(), {"Content-Type": f"multipart/form-data; boundary={boundary}"}), meta


# --- accounts
alice_t, alice = login("email", "alice@example.com")
bob_t, bob = login("phone", "+15550000002")
check("login alice (email) and bob (phone)", alice["username"] == "alice" and bob["username"] == "bob")
# API v1.2: dev servers stay open for UI tests and seed alice as the web-panel admin.
s, info, _ = req("GET", "/api/v1/server")
check("server info has registration + user_invites", info.get("registration") in ("open", "invite", "closed") and isinstance(info.get("user_invites"), bool), str(info))
check("me has is_admin + stats (alice is the dev admin)", alice.get("is_admin") is True and bob.get("is_admin") is False
      and isinstance(alice.get("stats", {}).get("moments"), int), str(alice.get("stats")))
s, b, _ = req("POST", "/api/v1/auth/login", body={"method": "email", "identifier": "alice@example.com", "password": "wrong-password", "device": DEV})
check("wrong password -> 401 invalid_credentials", s == 401 and b["error"]["code"] == "invalid_credentials")
dave_email = f"dave{uuid.uuid4().hex[:6]}@example.com"
s, b, _ = req("POST", "/api/v1/auth/register", body={"method": "email", "identifier": dave_email, "password": "hunter2hunter2", "display_name": "Dave", "invite_code": None, "device": DEV})
check("register stranger dave", s == 200 and b["is_new"] is True, f"{s} {b}")
dave_t = b["token"]
s, b, _ = req("PATCH", "/api/v1/me", dave_t, {"username": "dave" + uuid.uuid4().hex[:4]})
check("dave sets username", s == 200, f"{s} {b}")
s, b, _ = req("PATCH", "/api/v1/me", dave_t, {"username": "_bad..name"})
check("invalid username rejected", s == 400, f"{s} {b}")

# --- feed & media
s, feed, _ = req("GET", "/api/v1/moments?limit=30", alice_t)
check("alice feed has seeded moments", s == 200 and len(feed["moments"]) >= 5, f"{s} {len(feed.get('moments', []))}")
m0 = feed["moments"][0]
s, data, _ = req("GET", m0["thumb"]["url"], raw=True)
check("signed media URL loads without auth", s == 200 and data[:3] == b"\xff\xd8\xff", f"{s}")
bad = m0["thumb"]["url"][:-3] + ("AAA" if not m0["thumb"]["url"].endswith("AAA") else "BBB")
s, _, _ = req("GET", bad, raw=True)
check("tampered media signature -> 403", s == 403, f"{s}")
s, _, h = req("GET", m0["media"]["url"], raw=True, headers={"Range": "bytes=0-9"})
check("media Range request -> 206", s == 206, f"{s}")

# --- realtime: bob listens on SSE while alice posts
events = []
def sse():
    r = urllib.request.Request(BASE + "/api/v1/events", headers={"Authorization": "Bearer " + bob_t})
    try:
        with urllib.request.urlopen(r, timeout=8) as resp:
            ev = None
            for line in resp:
                line = line.decode().rstrip("\n")
                if line.startswith("event:"):
                    ev = line[6:].strip()
                elif line.startswith("data:") and ev:
                    events.append((ev, json.loads(line[5:].strip())))
    except Exception:
        pass
t = threading.Thread(target=sse, daemon=True); t.start(); time.sleep(0.5)

(s, mom, _), meta = post_moment(alice_t, caption={"type": "text", "text": "smoke test 🐦", "style": "classic", "color": None, "background": None, "data": None})
check("alice posts photo moment (multipart)", s == 201 and mom["kind"] == "photo", f"{s} {mom}")
mid = mom["id"]
(s2, mom2, _), _ = post_moment(alice_t)
(s3, again, _) = req("POST", "/api/v1/moments", alice_t, b"x", {"Content-Type": "multipart/form-data; boundary=zz"})
check("garbage multipart rejected with 4xx", 400 <= s3 < 500, f"{s3}")
time.sleep(1.0)
check("bob receives moment.new over SSE", any(e == "moment.new" and d["moment"]["id"] == mid for e, d in events), f"{[e for e,_ in events]}")

s, latest, h = req("GET", "/api/v1/moments/latest", bob_t)
check("bob /moments/latest = alice's newest", s == 200 and latest["moment"]["author"]["id"] == alice["id"], f"{s}")
etag = h.get("ETag")
s, _, _ = req("GET", "/api/v1/moments/latest", bob_t, headers={"If-None-Match": etag})
check("ETag If-None-Match -> 304", etag and s == 304, f"{etag} {s}")

# --- stranger authz
s, _, _ = req("GET", f"/api/v1/moments/{mid}", dave_t)
check("stranger cannot read moment (404)", s == 404, f"{s}")
s, b, _ = req("POST", f"/api/v1/conversations/{alice['id']}/messages", dave_t, {"client_id": str(uuid.uuid4()), "text": "hi", "reply_to_moment_id": None})
check("stranger cannot message (not_friends)", s == 403 and b["error"]["code"] == "not_friends", f"{s} {b}")
s, b, _ = req("PUT", f"/api/v1/moments/{mid}/reaction", dave_t, {"emoji": "💛"})
check("stranger cannot react", s in (403, 404), f"{s}")

# --- react + reply
s, _, _ = req("PUT", f"/api/v1/moments/{mid}/reaction", bob_t, {"emoji": "🔥"})
check("bob reacts 🔥", s == 204, f"{s}")
s, _, _ = req("PUT", f"/api/v1/moments/{mid}/reaction", alice_t, {"emoji": "🔥"})
check("cannot react to own moment", s in (400, 403), f"{s}")
s, msg, _ = req("POST", f"/api/v1/conversations/{alice['id']}/messages", bob_t, {"client_id": str(uuid.uuid4()), "text": "love it", "reply_to_moment_id": mid})
check("bob replies to moment", s == 201 and msg["moment"]["id"] == mid, f"{s} {msg}")
s, mine, _ = req("GET", f"/api/v1/moments/{mid}", alice_t)
check("author sees reaction", s == 200 and any(r["emoji"] == "🔥" for r in mine["reactions"]), f"{s}")
s, convs, _ = req("GET", "/api/v1/conversations", alice_t)
check("alice conversation with bob has unread", s == 200 and any(c["peer"]["id"] == bob["id"] and c["unread_count"] >= 1 for c in convs["conversations"]))

# --- audience self / selected
(s, priv, _), _ = post_moment(alice_t, audience="self")
s2, _, _ = req("GET", f"/api/v1/moments/{priv['id']}", bob_t)
check("'Just me' moment invisible to friend", s == 201 and s2 == 404, f"{s} {s2}")
(s, sel, _), _ = post_moment(alice_t, audience="selected", recipients=[bob["id"]])
carol_t, _ = login("email", "carol@example.com")
sb, _, _ = req("GET", f"/api/v1/moments/{sel['id']}", bob_t)
sc, _, _ = req("GET", f"/api/v1/moments/{sel['id']}", carol_t)
check("selected audience: bob yes, carol no", sb == 200 and sc == 404, f"{sb} {sc}")

# --- friendship flow: new friend does not see older moments
s, fr, _ = req("POST", "/api/v1/friends/requests", dave_t, {"user_id": alice["id"]})
check("dave sends friend request", s in (200, 201) and fr["status"] == "requested", f"{s} {fr}")
s, reqs, _ = req("GET", "/api/v1/friends/requests", alice_t)
rid = next(r["id"] for r in reqs["incoming"] if r["from"]["id"] == fr["request"]["from"]["id"])
s, _, _ = req("POST", f"/api/v1/friends/requests/{rid}/accept", alice_t)
check("alice accepts", s == 200, f"{s}")
s, _, _ = req("GET", f"/api/v1/moments/{mid}", dave_t)
check("new friend can't see pre-friendship moment", s == 404, f"{s}")
s, _, _ = req("POST", f"/api/v1/conversations/{alice['id']}/messages", dave_t, {"client_id": str(uuid.uuid4()), "text": "hi alice", "reply_to_moment_id": None})
check("new friend can message", s == 201, f"{s}")
s, _, _ = req("DELETE", f"/api/v1/friends/{alice['id']}", dave_t)
s, b, _ = req("POST", f"/api/v1/conversations/{alice['id']}/messages", dave_t, {"client_id": str(uuid.uuid4()), "text": "again", "reply_to_moment_id": None})
check("after unfriend, messaging blocked", s == 403, f"{s} {b}")

# --- delete moment removes media
s, _, _ = req("DELETE", f"/api/v1/moments/{mid}", bob_t)
check("non-author cannot delete", s in (403, 404), f"{s}")
s, _, _ = req("DELETE", f"/api/v1/moments/{mid}", alice_t)
check("author deletes moment", s == 204, f"{s}")
s, _, _ = req("GET", mom["media"]["url"], raw=True)
check("deleted moment's media gone (404)", s == 404, f"{s}")

failed = [n for n, ok in results if not ok]
print(f"\n{len(results) - len(failed)}/{len(results)} passed")
sys.exit(1 if failed else 0)
