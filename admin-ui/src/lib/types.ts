// Admin API objects (docs/ADMIN.md §7). Timestamps are ms since the epoch.

export type UserMini = { id: string; username: string | null; display_name: string };

export type AdminUser = {
  id: string;
  username: string | null;
  display_name: string;
  has_avatar: boolean;
  email_masked: string | null;
  phone_masked: string | null;
  auth_methods: string[];
  is_admin: boolean;
  disabled_at: number | null;
  created_at: number;
  last_seen_at: number | null;
  moments: number;
  storage_bytes: number;
  friends: number;
};

export type AdminUserDetail = AdminUser & {
  email: string | null;
  phone: string | null;
  discoverable: boolean;
  searchable: boolean;
  totp_enabled: boolean;
  invited_by: UserMini | null;
  server_invite: { id: string; note: string } | null;
};

export type AppSession = {
  id: string;
  device_name: string;
  platform: string;
  created_at: number;
  last_seen_at: number;
  push: boolean;
};

export type FriendInvite = {
  id: string;
  owner: UserMini;
  created_at: number;
  expires_at: number;
  uses: number;
};

export type InviteStatus = "active" | "used_up" | "expired" | "revoked";

export type ServerInvite = {
  id: string;
  code: string | null;
  url: string | null;
  note: string;
  max_uses: number;
  uses: number;
  created_at: number;
  expires_at: number | null;
  revoked_at: number | null;
  status: InviteStatus;
  created_by: UserMini | null;
  registered: { user: UserMini; at: number }[];
};

export type AuditEntry = {
  id: string;
  at: number;
  actor: { id: string | null; label: string };
  action: string;
  target: { type: string; id: string; label: string } | null;
  detail: string | null;
  ip: string;
};

export type SessionInfo = {
  admin: {
    id: string;
    username: string | null;
    display_name: string;
    email: string | null;
    phone: string | null;
    totp_enabled: boolean;
    recovery_codes_left: number;
  };
  server: { name: string; version: string; public_url: string | null };
  session: { id: string; created_at: number; expires_at: number; idle_timeout_ms: number };
};

export type Overview = {
  server: {
    name: string;
    version: string;
    started_at: number;
    registration: Registration;
    user_invites: boolean;
    storage: "local" | "s3";
    public_url: string | null;
    admin_mode: "on" | "local" | "off";
    admin_listen: boolean;
  };
  users: { total: number; active_7d: number; disabled: number; admins: number };
  content: { moments: number; messages: number };
  reports: { open: number };
  storage: { media_bytes: number; media_files: number };
  database: { bytes: number };
  push: { configured: boolean; relay_url: string | null; reachable: boolean | null; checked_at: number | null };
  recent_signups: AdminUser[];
  recent_actions: AuditEntry[];
};

export type Registration = "open" | "invite" | "closed";

export type SettingValue<T> = {
  value: T;
  default: T;
  locked: boolean;
  source: "default" | "panel" | "config";
};

export type Settings = {
  registration: SettingValue<Registration>;
  user_invites: SettingValue<boolean>;
  name: SettingValue<string>;
  max_video_seconds: SettingValue<number>;
  max_upload_mb: SettingValue<number>;
  max_friends: SettingValue<number>;
};

export type SettingKey = keyof Settings;

export type AdminSessionRow = {
  id: string;
  ip: string;
  user_agent: string;
  created_at: number;
  last_seen_at: number;
  current: boolean;
};

export type SecurityInfo = {
  totp_enabled: boolean;
  recovery_codes_left: number;
  sessions: AdminSessionRow[];
};

export type Paged<K extends string, T> = { [key in K]: T[] } & { total: number; page: number; per_page: number };

export type ReportReason = "spam" | "harassment" | "nudity" | "violence" | "self_harm" | "other";
export type ReportTargetType = "moment" | "message" | "user";
export type ReportResolution = "deleted_content" | "suspended_user" | "dismissed";

export type ReportUser = {
  id: string;
  username: string | null;
  display_name: string;
  has_avatar: boolean;
  suspended: boolean;
};

export type AdminReport = {
  id: string;
  target_type: ReportTargetType;
  content_exists: boolean;
  reason: ReportReason;
  note: string | null;
  created_at: number;
  status: "open" | "resolved";
  resolution: ReportResolution | null;
  resolved_at: number | null;
  resolved_by: UserMini | null;
  reporter: ReportUser | null; // null once the account is deleted
  reported_user: ReportUser | null;
};

/** Exactly the reported item (GET reports/{id} only); null once it's gone. */
export type ReportContent =
  | {
      type: "moment";
      id: string;
      kind: "photo" | "video";
      created_at: number;
      caption: { type: string; text: string } | null;
      media: { url: string; content_type: string; width: number; height: number; duration_ms: number | null } | null;
      thumb: { url: string };
    }
  | { type: "message"; id: string; created_at: number; text: string }
  | { type: "user"; id: string; created_at: number; user: ReportUser };

export type ReportList = Paged<"reports", AdminReport> & { open: number };
