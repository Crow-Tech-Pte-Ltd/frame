-- v1.1 security fixes (API.md changelog v1.1).

-- Single-use Apple/Google raw nonces: sha256(raw_nonce), kept until the ID
-- token's expiry (plus skew) so a token can't be replayed, even across
-- restarts.
CREATE TABLE used_nonces (
  hash       BLOB PRIMARY KEY,
  expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX used_nonces_expires ON used_nonces(expires_at);

-- remover unfriended or blocked removed. While present, redeeming one of the
-- remover's invite links only creates a normal friend request. Cleared when
-- the two become friends again.
CREATE TABLE friend_removals (
  remover_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  removed_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (remover_id, removed_id)
) WITHOUT ROWID;
CREATE INDEX friend_removals_removed ON friend_removals(removed_id);

-- Password-reset confirm attempts per user per 24 h window, independent of
-- how many codes were requested.
CREATE TABLE reset_limits (
  user_id      TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  window_start INTEGER NOT NULL,
  attempts     INTEGER NOT NULL
) WITHOUT ROWID;

-- Invites can be listed (GET /invites): the code is kept encrypted with a key
-- derived from secrets.json (lookup still uses code_hash), plus a use count.
ALTER TABLE invites ADD COLUMN code_enc BLOB;
ALTER TABLE invites ADD COLUMN uses INTEGER NOT NULL DEFAULT 0;
