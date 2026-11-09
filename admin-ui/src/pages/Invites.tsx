import * as React from "react";
import { Info, Loader2, QrCode, TicketPlus } from "lucide-react";

import { useAdmin, useToast } from "@/app-context";
import { del, get, post } from "@/lib/api";
import { Link } from "@/lib/router";
import type { FriendInvite, InviteStatus, ServerInvite } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { CopyButton, CopyField, EmptyState, ErrorBox, Field, FormError, PageHeader, Select, Spinner, Time, useResource } from "@/components/common";
import { ConfirmDialog } from "@/components/dialogs";
import { QRCode } from "@/components/qrcode";

const STATUS: Record<InviteStatus, { label: string; variant: BadgeProps["variant"] }> = {
  active: { label: "Active", variant: "success" },
  used_up: { label: "Used up", variant: "muted" },
  expired: { label: "Expired", variant: "muted" },
  revoked: { label: "Revoked", variant: "danger" },
};

const EXPIRY = [
  { value: "1", label: "1 day" },
  { value: "7", label: "7 days" },
  { value: "30", label: "30 days" },
  { value: "never", label: "Never" },
];

export function InvitesPage() {
  const session = useAdmin();
  const toast = useToast();
  const [showAll, setShowAll] = React.useState(false);
  const invites = useResource(
    (signal) => get<{ invites: ServerInvite[] }>(`invites?status=${showAll ? "all" : "active"}`, { signal }),
    [showAll],
  );
  const friendInvites = useResource((signal) => get<{ invites: FriendInvite[] }>("friend-invites", { signal }), []);
  const [createOpen, setCreateOpen] = React.useState(false);
  const [qrFor, setQrFor] = React.useState<ServerInvite | null>(null);
  const [revoke, setRevoke] = React.useState<ServerInvite | null>(null);
  const [revokeFriend, setRevokeFriend] = React.useState<FriendInvite | null>(null);

  return (
    <>
      <PageHeader
        title="Invites"
        description="A server invite lets someone create an account here. It doesn't make them anyone's friend — they add friends in the app."
        actions={
          <Button onClick={() => setCreateOpen(true)}>
            <TicketPlus aria-hidden="true" />
            New invite
          </Button>
        }
      />

      {!session.server.public_url ? (
        <Alert className="mb-6" role="status">
          <Info className="h-4 w-4" />
          <AlertDescription>
            <code className="font-azeret text-xs">FRAME_PUBLIC_URL</code> isn't set, so invite links use the address you
            opened this panel with ({window.location.origin}). Make sure your friends can reach that address.
          </AlertDescription>
        </Alert>
      ) : null}

      <section aria-labelledby="server-invites" className="mb-10">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
          <h2 id="server-invites" className="font-azeret text-lg font-bold">
            Server invites
          </h2>
          <div role="group" aria-label="Which invites" className="inline-flex rounded-md border-2 border-primary p-0.5">
            {[false, true].map((all) => (
              <button
                key={String(all)}
                type="button"
                aria-pressed={showAll === all}
                onClick={() => setShowAll(all)}
                className={cn(
                  "h-8 rounded-sm px-3 font-azeret text-xs font-bold focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  showAll === all ? "bg-primary text-primary-foreground" : "hover:bg-soft",
                )}
              >
                {all ? "All" : "Active"}
              </button>
            ))}
          </div>
        </div>
        {invites.error && !invites.data ? <ErrorBox error={invites.error} onRetry={invites.reload} /> : null}
        {!invites.data && invites.loading ? <Spinner /> : null}
        {invites.data ? (
          invites.data.invites.length === 0 ? (
            <EmptyState>{showAll ? "No invites yet." : "No active invites. Create one to bring someone in."}</EmptyState>
          ) : (
            <ul className="grid gap-4 md:grid-cols-2" aria-busy={invites.loading}>
              {invites.data.invites.map((inv) => (
                <li key={inv.id}>
                  <Card className="h-full">
                    <CardHeader className="pb-3">
                      <div className="flex items-start justify-between gap-3">
                        <CardTitle className="min-w-0 break-words text-base leading-snug">
                          {inv.note || <span className="text-muted-foreground">No note</span>}
                        </CardTitle>
                        <Badge variant={STATUS[inv.status].variant}>{STATUS[inv.status].label}</Badge>
                      </div>
                      <CardDescription>
                        <span className="font-azeret tabular">
                          {inv.uses} / {inv.max_uses}
                        </span>{" "}
                        used ·{" "}
                        {inv.status === "revoked" && inv.revoked_at ? (
                          <>
                            revoked <Time ms={inv.revoked_at} />
                          </>
                        ) : inv.expires_at ? (
                          <>
                            {inv.expires_at > Date.now() ? "expires" : "expired"} <Time ms={inv.expires_at} />
                          </>
                        ) : (
                          "never expires"
                        )}
                      </CardDescription>
                    </CardHeader>
                    <CardContent className="grid gap-4">
                      <p className="text-xs text-muted-foreground">
                        Created <Time ms={inv.created_at} />
                        {inv.created_by ? <> by {inv.created_by.display_name}</> : null}
                      </p>
                      {inv.registered.length > 0 ? (
                        <div>
                          <p className="mb-1 font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">Joined with it</p>
                          <ul className="flex flex-wrap gap-2">
                            {inv.registered.map((r) => (
                              <li key={r.user.id}>
                                <Link
                                  to={`/users/${encodeURIComponent(r.user.id)}`}
                                  className="inline-flex items-center rounded-full border border-primary px-2.5 py-0.5 text-xs font-bold hover:bg-soft"
                                  title={new Date(r.at).toLocaleString()}
                                >
                                  {r.user.display_name}
                                </Link>
                              </li>
                            ))}
                          </ul>
                        </div>
                      ) : null}
                      {inv.status === "active" ? (
                        <div className="flex flex-wrap gap-2">
                          {inv.url ? <CopyButton text={inv.url} label="Copy link" /> : null}
                          {inv.url ? (
                            <Button variant="outline" size="sm" onClick={() => setQrFor(inv)}>
                              <QrCode aria-hidden="true" />
                              QR code
                            </Button>
                          ) : null}
                          <Button variant="danger-outline" size="sm" onClick={() => setRevoke(inv)}>
                            Revoke
                          </Button>
                        </div>
                      ) : null}
                    </CardContent>
                  </Card>
                </li>
              ))}
            </ul>
          )
        ) : null}
      </section>

      <section aria-labelledby="friend-invites">
        <h2 id="friend-invites" className="font-azeret text-lg font-bold">
          Members' invite links
        </h2>
        <p className="mb-3 mt-1 max-w-2xl text-sm text-muted-foreground">
          Links your members shared from the app. They make the person a friend instantly
          {" — "}and can register new accounts when “Members can invite” is on in Settings.
        </p>
        {friendInvites.error && !friendInvites.data ? <ErrorBox error={friendInvites.error} onRetry={friendInvites.reload} /> : null}
        {!friendInvites.data && friendInvites.loading ? <Spinner /> : null}
        {friendInvites.data ? (
          friendInvites.data.invites.length === 0 ? (
            <EmptyState>No open member invite links.</EmptyState>
          ) : (
            <div className="overflow-x-auto rounded-lg border-2 border-primary bg-card shadow-brutal">
              <table className="w-full min-w-[34rem] text-left text-sm">
                <caption className="sr-only">Members' invite links</caption>
                <thead className="border-b-2 border-primary bg-kuning-100 font-azeret text-xs uppercase tracking-wide text-primary-400">
                  <tr>
                    <th scope="col" className="px-4 py-3">Member</th>
                    <th scope="col" className="px-4 py-3">Created</th>
                    <th scope="col" className="px-4 py-3">Expires</th>
                    <th scope="col" className="px-4 py-3 text-right">Uses</th>
                    <th scope="col" className="px-4 py-3">
                      <span className="sr-only">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {friendInvites.data.invites.map((i) => (
                    <tr key={i.id}>
                      <td className="px-4 py-3">
                        <Link to={`/users/${encodeURIComponent(i.owner.id)}`} className="font-bold hover:underline">
                          {i.owner.display_name}
                        </Link>
                        {i.owner.username ? <span className="ml-2 font-azeret text-xs text-muted-foreground">@{i.owner.username}</span> : null}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <Time ms={i.created_at} />
                      </td>
                      <td className="whitespace-nowrap px-4 py-3">
                        <Time ms={i.expires_at} />
                      </td>
                      <td className="px-4 py-3 text-right font-azeret tabular">{i.uses}</td>
                      <td className="px-4 py-3 text-right">
                        <Button variant="outline" size="sm" onClick={() => setRevokeFriend(i)}>
                          Revoke
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        ) : null}
      </section>

      <CreateInviteDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={invites.reload} />

      <AlertDialog open={qrFor !== null} onOpenChange={(o) => !o && setQrFor(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Invite QR code</AlertDialogTitle>
            <AlertDialogDescription>
              {qrFor?.note ? <>“{qrFor.note}” — </> : null}scan it with a phone camera to open the invite.
            </AlertDialogDescription>
          </AlertDialogHeader>
          {qrFor?.url ? (
            <div className="grid justify-items-center gap-4">
              <QRCode value={qrFor.url} label="QR code of the invite link" />
              <CopyField id="qr-invite-link" label="Invite link" value={qrFor.url} />
            </div>
          ) : null}
          <AlertDialogFooter>
            <AlertDialogCancel>Close</AlertDialogCancel>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <ConfirmDialog
        open={revoke !== null}
        onOpenChange={(o) => !o && setRevoke(null)}
        title="Revoke this invite?"
        description="The link stops working immediately. Accounts already created with it stay."
        confirmLabel="Revoke"
        destructive
        onConfirm={async () => {
          if (!revoke) return;
          await del(`invites/${revoke.id}`);
          toast("Invite revoked.");
          invites.reload();
        }}
      />
      <ConfirmDialog
        open={revokeFriend !== null}
        onOpenChange={(o) => !o && setRevokeFriend(null)}
        title={`Revoke ${revokeFriend?.owner.display_name ?? ""}'s invite link?`}
        description="Nobody can use it any more. Friendships already made through it stay."
        confirmLabel="Revoke"
        destructive
        onConfirm={async () => {
          if (!revokeFriend) return;
          await del(`friend-invites/${revokeFriend.id}`);
          toast("Invite link revoked.");
          friendInvites.reload();
        }}
      />
    </>
  );
}

function CreateInviteDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: () => void }) {
  const [maxUses, setMaxUses] = React.useState("1");
  const [expiry, setExpiry] = React.useState("7");
  const [note, setNote] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [created, setCreated] = React.useState<ServerInvite | null>(null);

  React.useEffect(() => {
    if (open) {
      setMaxUses("1");
      setExpiry("7");
      setNote("");
      setError(null);
      setCreated(null);
    }
  }, [open]);

  const uses = Number(maxUses);
  const usesValid = Number.isInteger(uses) && uses >= 1 && uses <= 1000;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!usesValid) return;
    setPending(true);
    setError(null);
    try {
      const r = await post<{ invite: ServerInvite }>("invites", {
        max_uses: uses,
        expires_in_days: expiry === "never" ? null : Number(expiry),
        note: note.trim(),
      });
      setCreated(r.invite);
      onCreated();
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <AlertDialogContent className="max-w-xl">
        {created ? (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Invite ready</AlertDialogTitle>
              <AlertDialogDescription>
                Send the link privately — anyone who has it can create an account until it's used up
                {created.expires_at ? " or expires" : ""}.
              </AlertDialogDescription>
            </AlertDialogHeader>
            {created.url ? (
              <div className="grid grid-cols-[minmax(0,1fr)] gap-4 sm:grid-cols-[auto_minmax(0,1fr)] sm:items-center">
                <div className="justify-self-center">
                  <QRCode value={created.url} label="QR code of the invite link" size={176} />
                </div>
                <div className="grid gap-3">
                  <CopyField id="created-invite-link" label="Invite link" value={created.url} />
                  <p className="text-xs text-muted-foreground">
                    {created.max_uses === 1 ? "One use" : `${created.max_uses} uses`} ·{" "}
                    {created.expires_at ? (
                      <>
                        expires <Time ms={created.expires_at} />
                      </>
                    ) : (
                      "never expires"
                    )}
                  </p>
                </div>
              </div>
            ) : null}
            <AlertDialogFooter>
              <AlertDialogCancel>Done</AlertDialogCancel>
            </AlertDialogFooter>
          </>
        ) : (
          <form className="grid gap-4" onSubmit={submit}>
            <AlertDialogHeader>
              <AlertDialogTitle>New server invite</AlertDialogTitle>
              <AlertDialogDescription>Lets someone create an account on this server.</AlertDialogDescription>
            </AlertDialogHeader>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field id="invite-uses" label="Max uses" hint="How many accounts can be created with it (1–1000).">
                <Input
                  id="invite-uses"
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={1000}
                  required
                  aria-invalid={!usesValid}
                  aria-describedby="invite-uses-hint"
                  value={maxUses}
                  onChange={(e) => setMaxUses(e.target.value)}
                  className="font-azeret"
                />
              </Field>
              <Field id="invite-expiry" label="Expires after">
                <Select id="invite-expiry" value={expiry} onChange={(e) => setExpiry(e.target.value)}>
                  {EXPIRY.map((x) => (
                    <option key={x.value} value={x.value}>
                      {x.label}
                    </option>
                  ))}
                </Select>
              </Field>
            </div>
            <Field id="invite-note" label="Note (optional)" hint="Only admins see it, e.g. “for Mum”.">
              <Textarea
                id="invite-note"
                rows={2}
                maxLength={200}
                aria-describedby="invite-note-hint"
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
            </Field>
            <FormError error={error} />
            <AlertDialogFooter>
              <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
              <Button type="submit" disabled={pending || !usesValid}>
                {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                Create invite
              </Button>
            </AlertDialogFooter>
          </form>
        )}
      </AlertDialogContent>
    </AlertDialog>
  );
}
