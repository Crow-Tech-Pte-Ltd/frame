// Human wording for audit-log actions (docs/ADMIN.md). Unknown actions are
// shown verbatim so new server actions still render.
import type { AuditEntry } from "@/lib/types";

const VERBS: Record<string, string> = {
  "setup.complete": "created the first admin account",
  login: "signed in to the panel",
  "login.success": "signed in to the panel",
  "login.failed": "failed to sign in",
  logout: "signed out of the panel",
  "user.create": "created the account",
  "user.reset_password": "reset the password of",
  "user.disable": "suspended",
  "user.enable": "unsuspended",
  "user.promote": "made admin",
  "user.demote": "removed admin from",
  "user.delete": "deleted the account",
  "user.session_revoke": "signed out a device of",
  "user.sessions_revoke": "signed out every device of",
  "invite.create": "created the server invite",
  "invite.revoke": "revoked the server invite",
  "friend_invite.revoke": "revoked a friend invite of",
  "settings.update": "changed settings",
  "password.change": "changed their password",
  "totp.enable": "turned on two-factor authentication",
  "totp.disable": "turned off two-factor authentication",
  "totp.reset": "reset two-factor authentication of",
  "recovery_codes.regenerate": "generated new recovery codes",
  "admin_session.revoke": "signed out an admin session",
  "maintenance.backup": "downloaded a database backup",
  "maintenance.cleanup": "ran the media cleanup",
  "setup_link.create": "created a setup link",
  "report.view": "opened the reported content of",
  "report.delete_content": "deleted the content of",
  "report.suspend_user": "suspended the author of",
  "report.dismiss": "dismissed",
};

export function actionVerb(action: string): string {
  return VERBS[action] ?? action.replace(/[._]/g, " ");
}

export function describe(e: AuditEntry): string {
  const target = e.target ? ` ${e.target.label}` : "";
  return `${e.actor.label} ${actionVerb(e.action)}${target}`;
}
