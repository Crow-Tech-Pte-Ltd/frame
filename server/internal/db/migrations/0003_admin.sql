-- v1.2: invite-only registration and the web admin panel (docs/ADMIN.md).

-- Server invites are created by admins in the panel or the CLI. They let
-- someone register (limited uses, optional expiry) but create no friendship.
-- The code is a capability: lookups use SHA-256(code); code_enc (AES-GCM, key
-- derived from secrets.json) lets admins copy an active link again.
CREATE TABLE server_invites (
  id         TEXT PRIMARY KEY,
  code_hash  BLOB NOT NULL UNIQUE,
  code_enc   BLOB NOT NULL,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  note       TEXT NOT NULL DEFAULT '',
  max_uses   INTEGER NOT NULL,
  uses       INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  expires_at INTEGER,                 -- NULL = never
  revoked_at INTEGER
);
CREATE INDEX server_invites_created_by ON server_invites(created_by);

ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN disabled_at INTEGER;     -- suspended since
ALTER TABLE users ADD COLUMN invited_by TEXT REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN server_invite_id TEXT REFERENCES server_invites(id) ON DELETE SET NULL;
CREATE INDEX users_admin ON users(is_admin) WHERE is_admin = 1;
CREATE INDEX users_invited_by ON users(invited_by) WHERE invited_by IS NOT NULL;
CREATE INDEX users_server_invite ON users(server_invite_id) WHERE server_invite_id IS NOT NULL;
CREATE INDEX users_created ON users(created_at);

-- Admin panel sessions (separate from app sessions); only SHA-256(token) is
-- stored. Idle timeout 30 min, absolute 12 h (enforced in code).
CREATE TABLE admin_sessions (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   BLOB NOT NULL UNIQUE,
  ip           TEXT NOT NULL,
  user_agent   TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);
CREATE INDEX admin_sessions_user ON admin_sessions(user_id);

-- TOTP (RFC 6238) for admins. The secret is AES-GCM encrypted with a key
-- derived from secrets.json; enabled_at is NULL while enrolment is pending.
-- last_step prevents reusing a code.
CREATE TABLE admin_totp (
  user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret_enc BLOB NOT NULL,
  enabled_at INTEGER,
  last_step  INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

-- Single-use recovery codes, stored as HMAC-SHA256(server key, user:code).
CREATE TABLE admin_recovery_codes (
  user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash BLOB NOT NULL,
  used_at   INTEGER,
  PRIMARY KEY (user_id, code_hash)
) WITHOUT ROWID;

-- Settings changed in the admin panel (keys: registration, user_invites,
-- name, max_video_seconds, max_upload_mb, max_friends). Values set in the
-- environment/config file take precedence and are locked in the panel.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
) WITHOUT ROWID;

-- Every admin action. No foreign keys: entries outlive the users involved.
CREATE TABLE admin_audit (
  id           TEXT PRIMARY KEY,
  actor_id     TEXT,
  actor_label  TEXT NOT NULL,
  action       TEXT NOT NULL,
  target_type  TEXT,
  target_id    TEXT,
  target_label TEXT,
  detail       TEXT,
  ip           TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);
CREATE INDEX admin_audit_created ON admin_audit(created_at);
