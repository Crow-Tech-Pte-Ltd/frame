-- v1.2.1: why an app session was revoked, so a suspended user's apps get
-- 401 account_disabled (and can say so) instead of a generic 401 unauthorized.
-- Only SHA-256(token) is kept, as in sessions. Rows exist only while the user
-- is suspended: they are dropped when the user is reinstated (and cascade on
-- account deletion), and a lookup also requires users.disabled_at to be set,
-- so unknown or merely stale tokens never learn anything.
CREATE TABLE revoked_sessions (
  token_hash BLOB PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  reason     TEXT NOT NULL,          -- 'account_disabled'
  revoked_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX revoked_sessions_user ON revoked_sessions(user_id);
