import * as React from "react";
import { Info, Loader2 } from "lucide-react";

import { useSession } from "@/app-context";
import { ApiError, get, post } from "@/lib/api";
import { navigate, useSearchParams } from "@/lib/router";
import type { SessionInfo } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AuthShell } from "@/components/auth-shell";
import { Field, FormError } from "@/components/common";

const REASONS: Record<string, string> = {
  expired: "Your session ended. Sign in again to continue.",
  forbidden: "This account is not an admin any more.",
  signed_out: "You're signed out.",
};

function safeNext(next: string | null): string {
  // Only in-panel paths ("/users?…"), never another origin.
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.startsWith("/login") || next.startsWith("/setup")) return "/";
  return next;
}

export function LoginPage() {
  const params = useSearchParams();
  const { session, setSession } = useSession();
  const next = safeNext(params.get("next"));
  const reason = params.get("reason");

  const [identifier, setIdentifier] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [needCode, setNeedCode] = React.useState(false);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [setupNeeded, setSetupNeeded] = React.useState(false);
  const [serverName, setServerName] = React.useState<string | null>(null);
  const codeRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    if (session && reason !== "forbidden") navigate(next, { replace: true });
  }, [session, next, reason]);

  React.useEffect(() => {
    get<{ needed: boolean; server_name: string }>("setup", { session: false })
      .then((s) => {
        setSetupNeeded(s.needed);
        setServerName(s.server_name);
      })
      .catch(() => {});
  }, []);

  React.useEffect(() => {
    if (needCode) codeRef.current?.focus();
  }, [needCode]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setPending(true);
    setError(null);
    try {
      const s = await post<SessionInfo>(
        "login",
        { identifier: identifier.trim(), password, totp_code: needCode ? code.trim() : null },
        { session: false },
      );
      setSession(s);
      navigate(next, { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.code === "totp_required") {
        setNeedCode(true);
      } else {
        setError(err);
      }
    } finally {
      setPending(false);
    }
  }

  return (
    <AuthShell
      title="Sign in"
      subtitle={serverName ? <>to manage <strong className="text-foreground">{serverName}</strong></> : "to manage this Frame server"}
    >
      {reason && REASONS[reason] ? (
        <Alert className="mb-5" role="status">
          <Info className="h-4 w-4" />
          <AlertDescription>{REASONS[reason]}</AlertDescription>
        </Alert>
      ) : null}
      {setupNeeded ? (
        <Alert variant="warning" className="mb-5" role="status">
          <Info className="h-4 w-4" />
          <AlertDescription>
            This server has no admin yet. Open the one-time setup link printed in the server's log, or run{" "}
            <code className="font-azeret text-xs">frame-server admin setup-link</code> to get a new one.
          </AlertDescription>
        </Alert>
      ) : null}
      <form className="grid gap-4" onSubmit={submit} noValidate={false}>
        <Field id="login-identifier" label="Email, phone or username">
          <Input
            id="login-identifier"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            autoFocus
            readOnly={needCode}
            value={identifier}
            onChange={(e) => setIdentifier(e.target.value)}
          />
        </Field>
        <Field id="login-password" label="Password">
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            required
            readOnly={needCode}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        {needCode ? (
          <Field
            id="login-code"
            label="Two-factor code"
            hint="The 6-digit code from your authenticator app, or one of your recovery codes."
          >
            <Input
              id="login-code"
              ref={codeRef}
              inputMode="text"
              autoComplete="one-time-code"
              autoCapitalize="none"
              spellCheck={false}
              required
              aria-describedby="login-code-hint"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              className="font-azeret text-lg tracking-[0.3em]"
            />
          </Field>
        ) : null}
        <FormError error={error} />
        <Button type="submit" size="lg" disabled={pending} className="mt-2 w-full">
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          {needCode ? "Verify and sign in" : "Sign in"}
        </Button>
        {needCode ? (
          <Button
            variant="link"
            className="h-auto justify-self-center p-0 text-xs"
            onClick={() => {
              setNeedCode(false);
              setCode("");
              setPassword("");
              setError(null);
            }}
          >
            Use a different account
          </Button>
        ) : null}
      </form>
      <p className="mt-6 text-xs text-muted-foreground">
        Only admin accounts can sign in here. Forgot your password? Run{" "}
        <code className="font-azeret">frame-server admin user reset-password &lt;username&gt;</code> on the server.
      </p>
    </AuthShell>
  );
}
