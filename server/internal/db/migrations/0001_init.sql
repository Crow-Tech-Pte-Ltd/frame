-- Frame schema v1. All timestamps are integer milliseconds since the Unix
-- epoch (UTC). IDs are ULIDs (TEXT) unless noted; ULIDs sort by creation time,
-- so "ORDER BY id" is chronological and id-based cursors are stable.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
) WITHOUT ROWID;

-- Every stored blob (photo, video, thumbnail, avatar). The storage key is the
-- media id. Rows that nothing references (moments.media_id/thumb_id,
-- users.avatar_media_id) are orphans and are swept after a grace period.
CREATE TABLE media (
  id           TEXT PRIMARY KEY,
  owner_id     TEXT REFERENCES users(id) ON DELETE SET NULL,
  content_type TEXT NOT NULL,
  bytes        INTEGER NOT NULL,
  width        INTEGER NOT NULL,
  height       INTEGER NOT NULL,
  duration_ms  INTEGER,
  created_at   INTEGER NOT NULL
);
CREATE INDEX media_owner ON media(owner_id);
CREATE INDEX media_created ON media(created_at);

CREATE TABLE users (
  id              TEXT PRIMARY KEY,
  username        TEXT UNIQUE,            -- lowercase; NULL until onboarding
  display_name    TEXT NOT NULL,
  email           TEXT UNIQUE,            -- lowercase
  phone           TEXT UNIQUE,            -- E.164
  password_hash   TEXT,                   -- argon2id PHC string
  apple_sub       TEXT UNIQUE,
  google_sub      TEXT UNIQUE,
  avatar_media_id TEXT REFERENCES media(id) ON DELETE SET NULL,
  discoverable    INTEGER NOT NULL DEFAULT 1,
  searchable      INTEGER NOT NULL DEFAULT 1,
  read_receipts   INTEGER NOT NULL DEFAULT 1,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX users_avatar ON users(avatar_media_id);

-- sha256("frame-contact-v1:" + normalized identifier) for discoverable users
-- only. Uploaded contact hashes are never stored.
CREATE TABLE contact_hashes (
  hash    BLOB PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX contact_hashes_user ON contact_hashes(user_id);

-- Opaque bearer sessions; only SHA-256(token) is stored.
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   BLOB NOT NULL UNIQUE,
  device_name  TEXT NOT NULL,
  platform     TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);

-- Push device, at most one per session.
CREATE TABLE devices (
  session_id         TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
  user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform           TEXT NOT NULL,
  relay_token        TEXT NOT NULL,
  widget_relay_token TEXT,
  push_key           BLOB NOT NULL,
  pref_moments       INTEGER NOT NULL,
  pref_messages      INTEGER NOT NULL,
  pref_reactions     INTEGER NOT NULL,
  pref_friends       INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL
);
CREATE INDEX devices_user ON devices(user_id);
CREATE INDEX devices_relay_token ON devices(relay_token);

-- Friendships are stored in both directions.
CREATE TABLE friendships (
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  friend_id  TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, friend_id)
) WITHOUT ROWID;
CREATE INDEX friendships_friend ON friendships(friend_id);

CREATE TABLE friend_requests (
  id         TEXT PRIMARY KEY,
  from_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  to_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  UNIQUE (from_id, to_id)
);
CREATE INDEX friend_requests_to ON friend_requests(to_id);

CREATE TABLE blocks (
  blocker_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  blocked_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (blocker_id, blocked_id)
) WITHOUT ROWID;
CREATE INDEX blocks_blocked ON blocks(blocked_id);

-- Invite codes are capabilities; only SHA-256(code) is stored.
CREATE TABLE invites (
  code_hash  BLOB PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX invites_user ON invites(user_id);
CREATE INDEX invites_expires ON invites(expires_at);

CREATE TABLE moments (
  id                 TEXT PRIMARY KEY,
  author_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id          TEXT NOT NULL,
  kind               TEXT NOT NULL,     -- photo | video
  audience           TEXT NOT NULL,     -- all | selected | self
  media_id           TEXT NOT NULL REFERENCES media(id),
  thumb_id           TEXT NOT NULL REFERENCES media(id),
  local_date         TEXT NOT NULL,     -- YYYY-MM-DD, author's local day
  caption_type       TEXT,              -- NULL = no caption
  caption_text       TEXT,
  caption_style      TEXT,
  caption_color      TEXT,
  caption_background TEXT,
  caption_data       TEXT,              -- compact JSON object or NULL
  created_at         INTEGER NOT NULL,
  UNIQUE (author_id, client_id)
);
CREATE INDEX moments_author_date ON moments(author_id, local_date);
CREATE INDEX moments_media ON moments(media_id);
CREATE INDEX moments_thumb ON moments(thumb_id);

-- Fan-out-on-write feed: one row per (viewer, moment), including the author's
-- own row. A moment is visible to a non-author iff their row exists AND they
-- are currently friends with the author (so unfriending revokes access and
-- re-friending restores it). viewed_at doubles as the "seen by" list.
CREATE TABLE feed_entries (
  user_id   TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  moment_id TEXT NOT NULL REFERENCES moments(id) ON DELETE CASCADE,
  author_id TEXT NOT NULL,
  hidden    INTEGER NOT NULL DEFAULT 0,  -- "delete for me"
  viewed_at INTEGER,
  PRIMARY KEY (user_id, moment_id)
) WITHOUT ROWID;
CREATE INDEX feed_moment ON feed_entries(moment_id);
CREATE INDEX feed_user_author ON feed_entries(user_id, author_id, moment_id);
CREATE INDEX feed_unseen ON feed_entries(user_id, author_id) WHERE viewed_at IS NULL AND hidden = 0;

CREATE TABLE reactions (
  moment_id  TEXT NOT NULL REFERENCES moments(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  emoji      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (moment_id, user_id)
) WITHOUT ROWID;
CREATE INDEX reactions_user ON reactions(user_id);

-- conv = smaller user id || ':' || larger user id.
-- moment_* are denormalised (no FK) so a reply keeps pointing at a deleted
-- moment and renders as {"deleted": true}.
-- read_at drives the reader's own unread counts; receipt_at is what the
-- sender sees (only set when the reader has read receipts enabled).
CREATE TABLE messages (
  id               TEXT PRIMARY KEY,
  client_id        TEXT NOT NULL,
  sender_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  recipient_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  conv             TEXT NOT NULL,
  text             TEXT NOT NULL,
  moment_id        TEXT,
  moment_author_id TEXT,
  moment_kind      TEXT,
  created_at       INTEGER NOT NULL,
  read_at          INTEGER,
  receipt_at       INTEGER,
  UNIQUE (sender_id, client_id)
);
CREATE INDEX messages_conv ON messages(conv, id);
CREATE INDEX messages_recipient ON messages(recipient_id);
CREATE INDEX messages_unread ON messages(recipient_id, sender_id) WHERE read_at IS NULL;

CREATE TABLE message_reactions (
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  emoji      TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (message_id, user_id)
) WITHOUT ROWID;
CREATE INDEX message_reactions_user ON message_reactions(user_id);

-- Per-user conversation index (both directions), maintained on send/unsend.
CREATE TABLE conversations (
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  peer_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  last_message_id TEXT,
  updated_at      INTEGER NOT NULL,
  PRIMARY KEY (user_id, peer_id)
) WITHOUT ROWID;
CREATE INDEX conversations_user_updated ON conversations(user_id, updated_at);
CREATE INDEX conversations_peer ON conversations(peer_id);

CREATE TABLE password_resets (
  user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  code_hash  BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts   INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
