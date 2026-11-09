import * as React from "react";
import { Loader2, TriangleAlert } from "lucide-react";

import { useSession, useToast } from "@/app-context";
import { ApiError, get, post } from "@/lib/api";
import { Link, navigate } from "@/lib/router";
import type { SessionInfo } from "@/lib/types";
import { passwordError } from "@/lib/validate";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { AuthShell } from "@/components/auth-shell";
import { Field, FormError, Spinner } from "@/components/common";
import { emptyIdentity, IdentityFields, identityErrors, type Identity } from "@/components/identity-fields";

type Phase = "checking" | "no_token" | "invalid" | "done" | "form";

// Read the one-time token from the URL fragment once, then drop it from the
// address bar (and browser history) so it isn't left lying around.
// Cached so React's StrictMode double-invocation can't lose it.
let cachedToken: string | null = null;
function takeToken(): string {
  if (cachedToken !== null) return cachedToken;
  const m = /(?:^#|&)t=([^&]+)/.exec(window.location.hash);
  if (window.location.hash) {
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
  }
  cachedToken = m ? decodeURIComponent(m[1]) : "";
  return cachedToken;
}

const NEW_LINK = (
  <>
    Run <code className="font-azeret text-xs">frame-server admin setup-link</code> on the server (or{" "}
    <code className="font-azeret text-xs">sudo frame-admin setup-link</code> with the systemd install) to print a new
    link. Links work once and expire after 24 hours.
  </>
);

export function SetupPage() {
  const { setSession } = useSession();
  const toast = useToast();
  const [token] = React.useState(takeToken);
  const [phase, setPhase] = React.useState<Phase>("checking");
  const [serverName, setServerName] = React.useState<string>("");
  const [identity, setIdentity] = React.useState<Identity>(emptyIdentity);
  const [password, setPassword] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [touched, setTouched] = React.useState(false);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);

  React.useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const s = await get<{ needed: boolean; server_name: string }>("setup", { session: false });
        if (cancelled) return;
        setServerName(s.server_name);
        if (!s.needed) return setPhase("done");
        if (!token) return setPhase("no_token");
        await post("setup/verify", { token }, { session: false });
        if (!cancelled) setPhase("form");
      } catch (err) {
        if (cancelled) return;
        if (err instanceof ApiError && err.code === "setup_done") setPhase("done");
        else if (err instanceof ApiError && err.code === "setup_token_invalid") setPhase("invalid");
        else {
          setError(err);
          setPhase("invalid");
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  const idErrors = identityErrors(identity, true);
  const pwError = passwordError(password);
  const confirmError = confirm !== password ? "The passwords don't match" : null;
  const valid = Object.keys(idErrors).length === 0 && !pwError && !confirmError;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (!valid) return;
    setPending(true);
    setError(null);
    try {
      const s = await post<SessionInfo>(
        "setup",
        {
          token,
          display_name: identity.display_name.trim(),
          username: identity.username,
          method: identity.method,
          identifier: identity.identifier.trim(),
          password,
        },
        { session: false },
      );
      setSession(s);
      toast("Your admin account is ready.");
      navigate("/", { replace: true });
    } catch (err) {
      if (err instanceof ApiError && err.code === "setup_token_invalid") setPhase("invalid");
      else if (err instanceof ApiError && err.code === "setup_done") setPhase("done");
      setError(err);
    } finally {
      setPending(false);
    }
  }

  if (phase === "checking") {
    return (
      <AuthShell title="Set up this server">
        <Spinner label="Checking your setup link…" className="py-4" />
      </AuthShell>
    );
  }

  if (phase === "done") {
    return (
      <AuthShell title="Already set up" subtitle={serverName ? <>{serverName} already has an admin.</> : undefined}>
        <p className="text-sm">
          The first admin account exists, so setup links no longer work. Sign in instead — or ask an existing admin to
          make you an admin.
        </p>
        <Button asChild className="mt-6 w-full">
          <Link to="/login">Go to sign-in</Link>
        </Button>
      </AuthShell>
    );
  }

  if (phase === "no_token" || phase === "invalid") {
    return (
      <AuthShell title="Set up this server">
        <Alert variant="warning">
          <TriangleAlert className="h-4 w-4" />
          <AlertTitle>{phase === "no_token" ? "You need the setup link" : "This setup link doesn't work"}</AlertTitle>
          <AlertDescription>
            {phase === "no_token" ? (
              <p>Open the one-time link from the server's log. It looks like …/admin/setup#t=… .</p>
            ) : (
              <p>It may have expired, been used already, or been replaced by a newer link.</p>
            )}
            <p className="mt-2">{NEW_LINK}</p>
          </AlertDescription>
        </Alert>
        {error && !(error instanceof ApiError && error.code === "setup_token_invalid") ? (
          <div className="mt-4">
            <FormError error={error} />
          </div>
        ) : null}
      </AuthShell>
    );
  }

  return (
    <AuthShell
      title="Create the admin account"
      subtitle={
        <>
          for <strong className="text-foreground">{serverName || "this server"}</strong>. This is also a normal Frame
          account: sign in with the same details in the iOS or Android app.
        </>
      }
    >
      <form className="grid gap-4" onSubmit={submit} noValidate>
        <IdentityFields
          idPrefix="setup"
          value={identity}
          onChange={setIdentity}
          errors={touched ? idErrors : {}}
          usernameRequired
        />
        <Field id="setup-password" label="Password" hint="At least 8 characters. A long passphrase is best.">
          <Input
            id="setup-password"
            type="password"
            autoComplete="new-password"
            required
            minLength={8}
            maxLength={128}
            aria-invalid={touched && !!pwError}
            aria-describedby="setup-password-hint"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          {touched && pwError ? <p className="text-xs font-medium text-destructive">{pwError}</p> : null}
        </Field>
        <Field id="setup-confirm" label="Repeat password">
          <Input
            id="setup-confirm"
            type="password"
            autoComplete="new-password"
            required
            aria-invalid={touched && !!confirmError}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {touched && confirmError ? <p className="text-xs font-medium text-destructive">{confirmError}</p> : null}
        </Field>
        <FormError error={error} />
        <Button type="submit" size="lg" disabled={pending} className="mt-2 w-full">
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
          Create admin account
        </Button>
        <p className="text-xs text-muted-foreground">
          Next, turn on two-factor authentication under Security. The panel will remind you.
        </p>
      </form>
    </AuthShell>
  );
}
