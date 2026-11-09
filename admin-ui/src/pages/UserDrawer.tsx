import * as React from "react";
import { Ban, Eye, EyeOff, KeyRound, LogOut, ShieldCheck, ShieldOff, Trash2, UserCheck, X } from "lucide-react";

import { useAdmin, useToast } from "@/app-context";
import { del, get, post } from "@/lib/api";
import { bytes, num } from "@/lib/format";
import { Link } from "@/lib/router";
import type { AdminUserDetail, AppSession, FriendInvite } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Avatar, CopyField, EmptyState, ErrorBox, Field, Spinner, Time, useResource } from "@/components/common";
import { ConfirmDialog } from "@/components/dialogs";
import { UserBadges } from "@/components/user-badges";

type Detail = { user: AdminUserDetail; sessions: AppSession[]; invites: FriendInvite[] };

type Pending =
  | null
  | "reset"
  | "disable"
  | "enable"
  | "promote"
  | "demote"
  | "delete"
  | "revoke-all"
  | { session: AppSession }
  | { invite: FriendInvite };

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-3 border-b border-border py-2 text-sm last:border-b-0">
      <dt className="font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

function Section({ title, children, action }: { title: string; children: React.ReactNode; action?: React.ReactNode }) {
  return (
    <section className="border-t-2 border-primary px-5 py-5 sm:px-6">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h3 className="font-azeret text-sm font-bold uppercase tracking-wide">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  );
}

export function UserDrawer({ userID, onClose, onChanged }: { userID: string | null; onClose: () => void; onChanged: () => void }) {
  return (
    <AlertDialog open={userID !== null} onOpenChange={(o) => !o && onClose()}>
      <AlertDialogContent layout="drawer" dismissOnBackdrop>
        {userID ? <DrawerBody userID={userID} onClose={onClose} onChanged={onChanged} /> : null}
      </AlertDialogContent>
    </AlertDialog>
  );
}

function DrawerBody({ userID, onClose, onChanged }: { userID: string; onClose: () => void; onChanged: () => void }) {
  const me = useAdmin().admin;
  const toast = useToast();
  const res = useResource((signal) => get<Detail>(`users/${encodeURIComponent(userID)}`, { signal }), [userID]);
  const [pending, setPending] = React.useState<Pending>(null);
  const [reveal, setReveal] = React.useState(false);
  const [oneTime, setOneTime] = React.useState<string | null>(null);
  const [confirmText, setConfirmText] = React.useState("");

  const d = res.data;
  const u = d?.user;
  const self = u?.id === me.id;
  const deleteWord = u ? (u.username ?? u.id) : "";

  const done = (msg: string) => {
    toast(msg);
    res.reload();
    onChanged();
  };

  const header = (
    <div className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b-2 border-primary bg-page px-5 py-4 sm:px-6">
      <h2 className="font-azeret text-lg font-bold">User</h2>
      <Button variant="outline" size="icon" onClick={onClose} aria-label="Close">
        <X aria-hidden="true" />
      </Button>
    </div>
  );

  if (!d || !u) {
    return (
      <div className="flex h-full flex-col">
        {header}
        <div className="p-6">{res.error ? <ErrorBox error={res.error} onRetry={res.reload} /> : <Spinner />}</div>
      </div>
    );
  }

  const contactValue = (full: string | null, masked: string | null) =>
    full ? <span className="font-azeret text-xs">{reveal ? full : masked ?? "•••"}</span> : <span className="text-muted-foreground">—</span>;

  return (
    <div className="flex min-h-full flex-col">
      {header}
      <div className="px-5 py-5 sm:px-6">
        <div className="flex items-center gap-4">
          <Avatar user={u} size="lg" />
          <div className="min-w-0 flex-1">
            <p className="truncate font-azeret text-xl font-bold">{u.display_name}</p>
            <p className="truncate font-azeret text-sm text-muted-foreground">{u.username ? "@" + u.username : "no username yet"}</p>
            <div className="mt-2 flex flex-wrap gap-1">
              <UserBadges user={u} />
              {self ? <Badge variant="info">You</Badge> : null}
            </div>
          </div>
        </div>

        <dl className="mt-5">
          <Fact label="Email">{contactValue(u.email, u.email_masked)}</Fact>
          <Fact label="Phone">{contactValue(u.phone, u.phone_masked)}</Fact>
          <Fact label="Sign-in">{u.auth_methods.length ? u.auth_methods.join(", ") : "—"}</Fact>
          <Fact label="Joined">
            <Time ms={u.created_at} mode="absolute" />
          </Fact>
          <Fact label="Last seen">
            <Time ms={u.last_seen_at} />
          </Fact>
          <Fact label="Joined via">
            {u.server_invite ? (
              <>Server invite{u.server_invite.note ? <> “{u.server_invite.note}”</> : null}</>
            ) : u.invited_by ? (
              <>
                Invite from{" "}
                <Link to={`/users/${encodeURIComponent(u.invited_by.id)}`} className="font-bold underline underline-offset-2">
                  {u.invited_by.display_name}
                </Link>
              </>
            ) : (
              <span className="text-muted-foreground">—</span>
            )}
          </Fact>
          <Fact label="Activity">
            <span className="font-azeret text-xs tabular">
              {num(u.moments)} moments · {bytes(u.storage_bytes)} · {num(u.friends)} friends
            </span>
          </Fact>
          <Fact label="Discovery">
            {u.searchable ? "In username search" : "Hidden from search"} ·{" "}
            {u.discoverable ? "matchable by contacts" : "not matchable by contacts"}
          </Fact>
          {u.is_admin ? <Fact label="Two-factor">{u.totp_enabled ? "On" : "Off"}</Fact> : null}
          {u.disabled_at ? (
            <Fact label="Suspended">
              <Time ms={u.disabled_at} mode="absolute" />
            </Fact>
          ) : null}
        </dl>
        {u.email || u.phone ? (
          <Button variant="ghost" size="sm" className="mt-2" onClick={() => setReveal((r) => !r)} aria-pressed={reveal}>
            {reveal ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
            {reveal ? "Hide contact details" : "Show contact details"}
          </Button>
        ) : null}
      </div>

      <Section title="Actions">
        {self ? (
          <p className="mb-3 text-sm text-muted-foreground">You can't suspend, demote or delete your own account here.</p>
        ) : null}
        <div className="grid gap-2 sm:grid-cols-2">
          <Button variant="outline" onClick={() => setPending("reset")} disabled={!u.email && !u.phone}>
            <KeyRound aria-hidden="true" />
            Reset password
          </Button>
          {u.disabled_at ? (
            <Button variant="outline" onClick={() => setPending("enable")} disabled={self}>
              <UserCheck aria-hidden="true" />
              Unsuspend
            </Button>
          ) : (
            <Button variant="outline" onClick={() => setPending("disable")} disabled={self}>
              <Ban aria-hidden="true" />
              Suspend
            </Button>
          )}
          {u.is_admin ? (
            <Button variant="outline" onClick={() => setPending("demote")} disabled={self}>
              <ShieldOff aria-hidden="true" />
              Remove admin
            </Button>
          ) : (
            <Button variant="outline" onClick={() => setPending("promote")} disabled={!!u.disabled_at}>
              <ShieldCheck aria-hidden="true" />
              Make admin
            </Button>
          )}
          <Button variant="danger-outline" onClick={() => setPending("delete")} disabled={self}>
            <Trash2 aria-hidden="true" />
            Delete account
          </Button>
        </div>
        {!u.email && !u.phone ? (
          <p className="mt-2 text-xs text-muted-foreground">
            This account signs in with Apple/Google only, so there is no password to reset.
          </p>
        ) : null}
      </Section>

      <Section
        title={`Devices (${d.sessions.length})`}
        action={
          d.sessions.length > 1 ? (
            <Button variant="outline" size="sm" onClick={() => setPending("revoke-all")}>
              <LogOut aria-hidden="true" />
              Sign out all
            </Button>
          ) : null
        }
      >
        {d.sessions.length === 0 ? (
          <EmptyState>Not signed in anywhere.</EmptyState>
        ) : (
          <ul className="grid gap-2">
            {d.sessions.map((s) => (
              <li key={s.id} className="flex items-center justify-between gap-3 rounded-md border-2 border-primary px-3 py-2">
                <div className="min-w-0">
                  <p className="truncate text-sm font-bold">{s.device_name}</p>
                  <p className="text-xs text-muted-foreground">
                    {s.platform} · active <Time ms={s.last_seen_at} /> · since <Time ms={s.created_at} />
                    {s.push ? " · push on" : ""}
                  </p>
                </div>
                <Button variant="outline" size="sm" onClick={() => setPending({ session: s })} aria-label={`Sign out ${s.device_name}`}>
                  Sign out
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={`Invite links (${d.invites.length})`}>
        {d.invites.length === 0 ? (
          <EmptyState>No open invite links.</EmptyState>
        ) : (
          <ul className="grid gap-2">
            {d.invites.map((i) => (
              <li key={i.id} className="flex items-center justify-between gap-3 rounded-md border-2 border-primary px-3 py-2">
                <p className="min-w-0 text-xs text-muted-foreground">
                  Created <Time ms={i.created_at} /> · expires <Time ms={i.expires_at} /> ·{" "}
                  <span className="font-azeret tabular">{i.uses}</span> {i.uses === 1 ? "use" : "uses"}
                </p>
                <Button variant="outline" size="sm" onClick={() => setPending({ invite: i })}>
                  Revoke
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Section>

      {/* Confirmations */}
      <ConfirmDialog
        open={pending === "reset"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Reset ${u.display_name}'s password?`}
        description="They get a new one-time password and are signed out of every device. Share it with them over a channel you trust."
        confirmLabel="Reset password"
        onConfirm={async () => {
          const r = await post<{ password: string }>(`users/${u.id}/reset-password`);
          setOneTime(r.password);
          done("Password reset.");
        }}
      />
      <ConfirmDialog
        open={pending === "disable"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Suspend ${u.display_name}?`}
        description="They're signed out everywhere, can't sign in, and disappear from search and contact matching. Their moments and messages stay. You can unsuspend them any time."
        confirmLabel="Suspend"
        destructive
        onConfirm={async () => {
          await post(`users/${u.id}/disable`);
          done(`${u.display_name} is suspended.`);
        }}
      />
      <ConfirmDialog
        open={pending === "enable"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Unsuspend ${u.display_name}?`}
        description="They can sign in again with their existing password."
        confirmLabel="Unsuspend"
        onConfirm={async () => {
          await post(`users/${u.id}/enable`);
          done(`${u.display_name} can sign in again.`);
        }}
      />
      <ConfirmDialog
        open={pending === "promote"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Make ${u.display_name} an admin?`}
        description="Admins can open this panel: manage every account, invites and settings, and download backups."
        confirmLabel="Make admin"
        onConfirm={async () => {
          await post(`users/${u.id}/promote`);
          done(`${u.display_name} is now an admin.`);
        }}
      />
      <ConfirmDialog
        open={pending === "demote"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Remove admin from ${u.display_name}?`}
        description="They keep their Frame account but can no longer open this panel. Their panel sessions end immediately."
        confirmLabel="Remove admin"
        onConfirm={async () => {
          await post(`users/${u.id}/demote`);
          done(`${u.display_name} is no longer an admin.`);
        }}
      />
      <ConfirmDialog
        open={pending === "revoke-all"}
        onOpenChange={(o) => !o && setPending(null)}
        title="Sign out every device?"
        description={`${u.display_name} will have to sign in again on all their phones.`}
        confirmLabel="Sign out all"
        onConfirm={async () => {
          await del(`users/${u.id}/sessions`);
          done("Signed out of every device.");
        }}
      />
      <ConfirmDialog
        open={pending !== null && typeof pending === "object" && "session" in pending}
        onOpenChange={(o) => !o && setPending(null)}
        title="Sign out this device?"
        description={
          pending && typeof pending === "object" && "session" in pending
            ? `${pending.session.device_name} will have to sign in again.`
            : ""
        }
        confirmLabel="Sign out"
        onConfirm={async () => {
          if (pending && typeof pending === "object" && "session" in pending) {
            await del(`users/${u.id}/sessions/${pending.session.id}`);
            done("Device signed out.");
          }
        }}
      />
      <ConfirmDialog
        open={pending !== null && typeof pending === "object" && "invite" in pending}
        onOpenChange={(o) => !o && setPending(null)}
        title="Revoke this invite link?"
        description="Nobody can use it any more. Friendships already made through it stay."
        confirmLabel="Revoke"
        destructive
        onConfirm={async () => {
          if (pending && typeof pending === "object" && "invite" in pending) {
            await del(`friend-invites/${pending.invite.id}`);
            done("Invite link revoked.");
          }
        }}
      />
      <ConfirmDialog
        open={pending === "delete"}
        onOpenChange={(o) => {
          if (!o) {
            setPending(null);
            setConfirmText("");
          }
        }}
        title={`Delete ${u.display_name}?`}
        description="This permanently deletes the account and all of its moments, media, messages and friendships. It can't be undone (except from an older backup)."
        confirmLabel="Delete forever"
        destructive
        confirmDisabled={confirmText.trim() !== deleteWord}
        onConfirm={async () => {
          await del(`users/${u.id}`, { confirm: confirmText.trim() });
          setConfirmText("");
          toast(`${u.display_name} was deleted.`);
          onChanged();
          onClose();
        }}
      >
        <Field id="delete-confirm" label={<>Type <span className="normal-case">{deleteWord}</span> to confirm</>}>
          <Input
            id="delete-confirm"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            className="font-azeret"
          />
        </Field>
      </ConfirmDialog>

      {/* One-time password result */}
      <AlertDialog open={oneTime !== null} onOpenChange={(o) => !o && setOneTime(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>New one-time password</AlertDialogTitle>
            <AlertDialogDescription>
              Give it to {u.display_name}. They should change it in the app after signing in.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {oneTime ? <CopyField id="reset-password" label="One-time password" value={oneTime} /> : null}
          <Alert variant="warning" role="status">
            <AlertDescription>This password is shown only once.</AlertDescription>
          </Alert>
          <AlertDialogFooter>
            <AlertDialogCancel>Done</AlertDialogCancel>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
