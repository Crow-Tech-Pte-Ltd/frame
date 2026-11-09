import * as React from "react";
import { Download, KeyRound, Loader2, LogOut, ShieldCheck, ShieldOff } from "lucide-react";

import { useSession, useToast } from "@/app-context";
import { del, get, post } from "@/lib/api";
import type { AdminSessionRow, SecurityInfo } from "@/lib/types";
import { passwordError } from "@/lib/validate";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { CopyButton, EmptyState, ErrorBox, Field, FormError, PageHeader, Spinner, Time, useResource } from "@/components/common";
import { ConfirmDialog, PasswordDialog } from "@/components/dialogs";
import { QRCode } from "@/components/qrcode";

export function SecurityPage() {
  const res = useResource((signal) => get<SecurityInfo>("security", { signal }), []);
  return (
    <>
      <PageHeader title="Security" description="Your admin account: password, two-factor authentication and panel sessions." />
      {res.error && !res.data ? <ErrorBox error={res.error} onRetry={res.reload} /> : null}
      {!res.data ? (
        res.error ? null : <Spinner />
      ) : (
        <div className="grid gap-6 xl:grid-cols-2">
          <TwoFactorCard info={res.data} onChanged={res.reload} />
          <PasswordCard />
          <SessionsCard sessions={res.data.sessions} onChanged={res.reload} />
        </div>
      )}
    </>
  );
}

function PasswordCard() {
  const toast = useToast();
  const [current, setCurrent] = React.useState("");
  const [next, setNext] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [touched, setTouched] = React.useState(false);
  const pwErr = passwordError(next);
  const mismatch = confirm !== next;

  return (
    <Card>
      <CardHeader>
        <CardTitle>Password</CardTitle>
        <CardDescription>
          This is also your password in the Frame app. Changing it signs out your other panel sessions and all your
          phones.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-4"
          noValidate
          onSubmit={async (e) => {
            e.preventDefault();
            setTouched(true);
            if (pwErr || mismatch || !current) return;
            setPending(true);
            setError(null);
            try {
              await post("security/password", { current_password: current, new_password: next });
              setCurrent("");
              setNext("");
              setConfirm("");
              setTouched(false);
              toast("Password changed. Your other sessions were signed out.");
            } catch (err) {
              setError(err);
            } finally {
              setPending(false);
            }
          }}
        >
          <Field id="pw-current" label="Current password">
            <Input id="pw-current" type="password" autoComplete="current-password" required value={current} onChange={(e) => setCurrent(e.target.value)} />
          </Field>
          <Field id="pw-new" label="New password" hint="At least 8 characters.">
            <Input
              id="pw-new"
              type="password"
              autoComplete="new-password"
              required
              aria-invalid={touched && !!pwErr}
              aria-describedby="pw-new-hint"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            {touched && pwErr ? <p className="text-xs font-medium text-destructive">{pwErr}</p> : null}
          </Field>
          <Field id="pw-confirm" label="Repeat new password">
            <Input
              id="pw-confirm"
              type="password"
              autoComplete="new-password"
              required
              aria-invalid={touched && mismatch}
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
            {touched && mismatch ? <p className="text-xs font-medium text-destructive">The passwords don't match</p> : null}
          </Field>
          <FormError error={error} />
          <Button type="submit" disabled={pending} className="justify-self-start">
            {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <KeyRound aria-hidden="true" />}
            Change password
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function RecoveryCodes({ codes, onDone, doneLabel }: { codes: string[]; onDone: () => void; doneLabel: string }) {
  const [href, setHref] = React.useState<string | null>(null);
  const text = codes.join("\n");
  React.useEffect(() => {
    const blob = new Blob(
      [
        "Frame admin recovery codes\n\nEach code works once, instead of a code from your authenticator app.\n\n" +
          text +
          "\n",
      ],
      { type: "text/plain" },
    );
    const url = URL.createObjectURL(blob);
    setHref(url);
    return () => URL.revokeObjectURL(url);
  }, [text]);
  return (
    <div className="grid gap-4">
      <Alert variant="warning" role="status">
        <AlertDescription>
          Save these recovery codes somewhere safe (a password manager is ideal). Each works once if you lose your
          phone. <strong>They're shown only now.</strong>
        </AlertDescription>
      </Alert>
      <ol className="grid grid-cols-2 gap-2 rounded-md border-2 border-primary bg-soft p-4 font-azeret text-sm text-soft-foreground" aria-label="Recovery codes">
        {codes.map((c) => (
          <li key={c} className="tabular">
            {c}
          </li>
        ))}
      </ol>
      <div className="flex flex-wrap gap-2">
        <CopyButton text={text} label="Copy codes" />
        {href ? (
          <Button asChild variant="outline" size="sm">
            <a href={href} download="frame-admin-recovery-codes.txt">
              <Download aria-hidden="true" />
              Download .txt
            </a>
          </Button>
        ) : null}
        <Button size="sm" onClick={onDone}>
          {doneLabel}
        </Button>
      </div>
    </div>
  );
}

type Enrol = { step: "idle" } | { step: "scan"; secret: string; uri: string } | { step: "codes"; codes: string[] };

function TwoFactorCard({ info, onChanged }: { info: SecurityInfo; onChanged: () => void }) {
  const { refresh } = useSession();
  const toast = useToast();
  const [enrol, setEnrol] = React.useState<Enrol>({ step: "idle" });
  const [askPassword, setAskPassword] = React.useState<null | "setup" | "regen" | "disable">(null);
  const [code, setCode] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [newCodes, setNewCodes] = React.useState<string[] | null>(null);

  const finish = () => {
    setEnrol({ step: "idle" });
    setNewCodes(null);
    onChanged();
    void refresh();
  };

  const secretGroups = enrol.step === "scan" ? enrol.secret.replace(/(.{4})/g, "$1 ").trim() : "";

  return (
    <Card className="xl:row-span-2">
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>Two-factor authentication</CardTitle>
          {info.totp_enabled ? (
            <Badge variant="success">
              <ShieldCheck className="h-3 w-3" aria-hidden="true" />
              On
            </Badge>
          ) : (
            <Badge variant="warning">Off</Badge>
          )}
        </div>
        <CardDescription>
          Asks for a 6-digit code from an authenticator app (Aegis, 1Password, Google Authenticator…) when you sign in
          to this panel. The Frame app itself isn't affected.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {enrol.step === "scan" ? (
          <form
            className="grid gap-5"
            onSubmit={async (e) => {
              e.preventDefault();
              setPending(true);
              setError(null);
              try {
                const r = await post<{ recovery_codes: string[] }>("security/totp/enable", { code: code.replace(/\s/g, "") });
                setCode("");
                setEnrol({ step: "codes", codes: r.recovery_codes });
                toast("Two-factor authentication is on.");
                // It's on now: update the badge and the reminder banner
                // while the codes stay on screen.
                onChanged();
                void refresh();
              } catch (err) {
                setError(err);
              } finally {
                setPending(false);
              }
            }}
          >
            <ol className="grid list-decimal gap-2 pl-5 text-sm">
              <li>Open your authenticator app and add an account by scanning this QR code.</li>
              <li>Enter the 6-digit code it shows to finish.</li>
            </ol>
            <div className="grid gap-4 sm:grid-cols-[auto_minmax(0,1fr)] sm:items-start">
              <div className="justify-self-center">
                <QRCode value={enrol.uri} label="QR code for your authenticator app" size={184} />
              </div>
              <div className="grid content-start gap-2">
                <p className="font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">Can't scan? Enter this key</p>
                <p className="break-all rounded-md border-2 border-primary bg-soft px-3 py-2 font-azeret text-sm text-soft-foreground">{secretGroups}</p>
                <CopyButton text={enrol.secret} label="Copy key" className="justify-self-start" />
                <p className="text-xs text-muted-foreground">Time-based (TOTP), SHA-1, 6 digits, 30 seconds.</p>
              </div>
            </div>
            <Field id="totp-code" label="Code from the app">
              <Input
                id="totp-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9 ]*"
                maxLength={7}
                required
                autoFocus
                value={code}
                onChange={(e) => setCode(e.target.value)}
                className="max-w-[12rem] font-azeret text-lg tracking-[0.3em]"
              />
            </Field>
            <FormError error={error} />
            <div className="flex flex-wrap gap-2">
              <Button type="submit" disabled={pending || code.replace(/\s/g, "").length !== 6}>
                {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                Turn on
              </Button>
              <Button variant="outline" onClick={() => setEnrol({ step: "idle" })} disabled={pending}>
                Cancel
              </Button>
            </div>
          </form>
        ) : enrol.step === "codes" ? (
          <RecoveryCodes codes={enrol.codes} onDone={finish} doneLabel="I've saved them" />
        ) : newCodes ? (
          <RecoveryCodes codes={newCodes} onDone={finish} doneLabel="Done" />
        ) : info.totp_enabled ? (
          <div className="grid gap-4">
            <p className="text-sm">
              Recovery codes left: <span className="font-azeret font-bold tabular">{info.recovery_codes_left}</span>
              {info.recovery_codes_left <= 2 ? <span className="text-destructive"> — generate new ones soon.</span> : null}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => setAskPassword("regen")}>
                New recovery codes
              </Button>
              <Button variant="danger-outline" onClick={() => setAskPassword("disable")}>
                <ShieldOff aria-hidden="true" />
                Turn off
              </Button>
            </div>
          </div>
        ) : (
          <div className="grid gap-4">
            <p className="text-sm">
              Without it, anyone who learns your password can open this panel and manage every account on this
              server.
            </p>
            <Button className="justify-self-start" onClick={() => setAskPassword("setup")}>
              <ShieldCheck aria-hidden="true" />
              Turn on two-factor
            </Button>
          </div>
        )}
      </CardContent>

      <PasswordDialog
        open={askPassword === "setup"}
        onOpenChange={(o) => !o && setAskPassword(null)}
        title="Confirm it's you"
        description="Enter your password to set up two-factor authentication."
        confirmLabel="Continue"
        onSubmit={async (password) => {
          const r = await post<{ secret: string; uri: string }>("security/totp/setup", { password });
          setError(null);
          setCode("");
          setEnrol({ step: "scan", secret: r.secret, uri: r.uri });
        }}
      />
      <PasswordDialog
        open={askPassword === "regen"}
        onOpenChange={(o) => !o && setAskPassword(null)}
        title="New recovery codes"
        description="Your old recovery codes stop working."
        confirmLabel="Generate"
        onSubmit={async (password) => {
          const r = await post<{ recovery_codes: string[] }>("security/recovery-codes", { password });
          setNewCodes(r.recovery_codes);
        }}
      />
      <PasswordDialog
        open={askPassword === "disable"}
        onOpenChange={(o) => !o && setAskPassword(null)}
        title="Turn off two-factor?"
        description="You'll only need your password to sign in to this panel."
        confirmLabel="Turn off"
        withCode
        onSubmit={async (password, c) => {
          await post("security/totp/disable", { password, code: c });
          toast("Two-factor authentication is off.");
          finish();
        }}
      />
    </Card>
  );
}

function shortUA(ua: string): string {
  const browser = /(Firefox|Edg|Chrome|Safari)\/[\d.]+/.exec(ua)?.[1]?.replace("Edg", "Edge") ?? "Browser";
  const os = /(Windows|Android|iPhone|iPad|Mac OS X|Linux)/.exec(ua)?.[1]?.replace("Mac OS X", "macOS") ?? "";
  return os ? `${browser} on ${os}` : browser;
}

function SessionsCard({ sessions, onChanged }: { sessions: AdminSessionRow[]; onChanged: () => void }) {
  const toast = useToast();
  const [revoke, setRevoke] = React.useState<AdminSessionRow | null>(null);
  return (
    <Card>
      <CardHeader>
        <CardTitle>Panel sessions</CardTitle>
        <CardDescription>Where you're signed in to this admin panel. Sessions end after 30 minutes idle or 12 hours.</CardDescription>
      </CardHeader>
      <CardContent>
        {sessions.length === 0 ? (
          <EmptyState>No sessions.</EmptyState>
        ) : (
          <ul className="grid gap-2">
            {sessions.map((s) => (
              <li key={s.id} className="flex items-center justify-between gap-3 rounded-md border-2 border-primary px-3 py-2">
                <div className="min-w-0">
                  <p className="flex flex-wrap items-center gap-2 text-sm font-bold">
                    <span className="truncate" title={s.user_agent}>
                      {shortUA(s.user_agent)}
                    </span>
                    {s.current ? <Badge variant="info">This browser</Badge> : null}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    <span className="font-azeret">{s.ip}</span> · active <Time ms={s.last_seen_at} /> · signed in <Time ms={s.created_at} />
                  </p>
                </div>
                {!s.current ? (
                  <Button variant="outline" size="sm" onClick={() => setRevoke(s)}>
                    <LogOut aria-hidden="true" />
                    Sign out
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <ConfirmDialog
        open={revoke !== null}
        onOpenChange={(o) => !o && setRevoke(null)}
        title="Sign out this session?"
        description={revoke ? `${shortUA(revoke.user_agent)} (${revoke.ip}) will have to sign in again.` : ""}
        confirmLabel="Sign out"
        onConfirm={async () => {
          if (!revoke) return;
          await del(`security/sessions/${revoke.id}`);
          toast("Session signed out.");
          onChanged();
        }}
      />
    </Card>
  );
}
