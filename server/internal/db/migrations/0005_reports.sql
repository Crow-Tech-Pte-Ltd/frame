-- v1.3: user reports (API.md "Reports", ADMIN.md "Reports"). A member reports
-- a moment, message or user they can see; the server's admin reviews it in the
-- panel. Nothing is scanned automatically. The target has no foreign key: a
-- report row outlives the reported content (the admin may delete it, or its
-- author may), and only ids are kept, never a copy of the content. People are
-- kept as ids that turn NULL when the account is deleted.
CREATE TABLE reports (
  id          TEXT PRIMARY KEY,
  reporter_id TEXT REFERENCES users(id) ON DELETE SET NULL,
  target_type TEXT NOT NULL,          -- moment | message | user
  target_id   TEXT NOT NULL,
  owner_id    TEXT REFERENCES users(id) ON DELETE SET NULL, -- author, sender or the reported user
  reason      TEXT NOT NULL,          -- spam | harassment | nudity | violence | self_harm | other
  note        TEXT,                   -- reporter's words, ≤ 500 characters
  created_at  INTEGER NOT NULL,
  status      TEXT NOT NULL DEFAULT 'open',  -- open | resolved
  resolution  TEXT,                   -- deleted_content | suspended_user | dismissed
  resolved_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  resolved_at INTEGER
);
CREATE INDEX reports_status ON reports(status, created_at);
CREATE INDEX reports_target ON reports(target_type, target_id);
CREATE INDEX reports_reporter ON reports(reporter_id);
CREATE INDEX reports_owner ON reports(owner_id);
CREATE INDEX reports_resolved_by ON reports(resolved_by) WHERE resolved_by IS NOT NULL;
-- One open report per reporter and target: reporting the same thing again
-- while it is open is a no-op.
CREATE UNIQUE INDEX reports_open_once ON reports(reporter_id, target_type, target_id) WHERE status = 'open';
