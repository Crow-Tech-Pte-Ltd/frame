"""Re-checks security-review findings (invite re-befriend, SSE after admin revocation). Usage: python3 scripts/security-recheck.py BASE_URL SERVER_BIN DATA_DIR (server must run in dev mode on DATA_DIR)."""
import json, subprocess, sys, threading, time, uuid, urllib.request, urllib.error

BASE, SERVER_BIN, DATA_DIR = sys.argv[1], sys.argv[2], sys.argv[3]
ok_all = True


def check(name, cond, detail=""):
    global ok_all
    ok_all &= bool(cond)
    print(("PASS " if cond else "FAIL ") + name + ("" if cond else f"  [{detail}]"))


def req(method, path, token=None, body=None):
    h = {"Content-Type": "application/json"} if body is not None else {}
    if token:
        h["Authorization"] = "Bearer " + token
    r = urllib.request.Request(BASE + path, data=json.dumps(body).encode() if body is not None else None, method=method, headers=h)
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            c = resp.read()
            return resp.status, json.loads(c) if c else None
    except urllib.error.HTTPError as e:
        c = e.read()
        return e.code, json.loads(c) if c else None


def login(ident):
    s, b = req("POST", "/api/v1/auth/login", body={"method": "email", "identifier": ident, "password": "password123", "device": {"name": "x", "platform": "ios"}})
    assert s == 200, (s, b)
    return b["token"], b["user"]


alice_t, alice = login("alice@example.com")
bob_t, bob = login("bob@example.com")

# --- Finding 2: invite re-befriend after unfriend
s, inv = req("POST", "/api/v1/invites", alice_t)
code = inv["code"]
s, feed = req("GET", f"/api/v1/moments?user_id={alice['id']}", bob_t)
n_before = len(feed["moments"])
req("DELETE", f"/api/v1/friends/{bob['id']}", alice_t)
s, feed = req("GET", f"/api/v1/moments?user_id={alice['id']}", bob_t)
check("after unfriend bob sees none of alice's moments", s in (200, 403, 404) and (s != 200 or len(feed["moments"]) == 0), f"{s}")
s, red = req("POST", f"/api/v1/invites/{code}/redeem", bob_t)
check("old invite after unfriend -> only a friend request", s in (200, 201) and red["status"] == "requested", f"{s} {red}")
s, feed = req("GET", f"/api/v1/moments?user_id={alice['id']}", bob_t)
check("bob still cannot see alice's moments", s != 200 or len(feed["moments"]) == 0, f"{s} {len(feed.get('moments', [])) if feed else ''}")
s, invs = req("GET", "/api/v1/invites", alice_t)
check("alice can list invites", s == 200 and any(i["code"] == code for i in invs["invites"]), f"{s}")
s, _ = req("DELETE", f"/api/v1/invites/{code}", alice_t)
s2, _ = req("GET", f"/api/v1/invites/{code}")
check("revoked invite no longer resolves", s == 204 and s2 == 404, f"{s} {s2}")
# alice accepts bob's request again -> friends; moments visible again (pre-existing behaviour)
s, reqs = req("GET", "/api/v1/friends/requests", alice_t)
rid = next(r["id"] for r in reqs["incoming"] if r["from"]["id"] == bob["id"])
s, _ = req("POST", f"/api/v1/friends/requests/{rid}/accept", alice_t)
s, feed = req("GET", f"/api/v1/moments?user_id={alice['id']}", bob_t)
check("after alice explicitly accepts, access restored", s == 200 and len(feed["moments"]) == n_before, f"{s} {len(feed['moments'])} vs {n_before}")

# --- Finding 3: admin CLI reset kills live SSE
got = []
closed = threading.Event()
def sse():
    r = urllib.request.Request(BASE + "/api/v1/events", headers={"Authorization": "Bearer " + bob_t})
    try:
        with urllib.request.urlopen(r, timeout=60) as resp:
            for line in resp:
                line = line.decode()
                if line.startswith("data:"):
                    got.append(line)
    except Exception:
        pass
    closed.set()
threading.Thread(target=sse, daemon=True).start()
time.sleep(0.5)
out = subprocess.run([SERVER_BIN, "admin", "user", "reset-password", "bob"], env={"FRAME_DATA_DIR": DATA_DIR, "PATH": "/usr/bin:/bin"}, capture_output=True, text=True)
check("admin reset-password ran", out.returncode == 0, out.stderr[-300:])
s, _ = req("GET", "/api/v1/me", bob_t)
check("bob's old token rejected (401)", s == 401, f"{s}")
n_before_msg = len(got)
req("POST", f"/api/v1/conversations/{bob['id']}/messages", alice_t, {"client_id": str(uuid.uuid4()), "text": "secret after reset", "reply_to_moment_id": None})
closed.wait(35)
leaked = any("secret after reset" in g for g in got[n_before_msg:])
check("SSE stream closed within one ping (≤35 s)", closed.is_set(), "still open")
check("no message leaked to revoked stream", not leaked, "leaked")
sys.exit(0 if ok_all else 1)
