import * as React from "react";
import { Loader2, Lock, RotateCcw } from "lucide-react";

import { useSession, useToast } from "@/app-context";
import { get, patch } from "@/lib/api";
import type { Registration, SettingKey, Settings, SettingValue } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorBox, FormError, PageHeader, Spinner, useResource } from "@/components/common";

type Draft = {
  registration: Registration;
  user_invites: boolean;
  name: string;
  max_video_seconds: string;
  max_upload_mb: string;
  max_friends: string;
};

function toDraft(s: Settings): Draft {
  return {
    registration: s.registration.value,
    user_invites: s.user_invites.value,
    name: s.name.value,
    max_video_seconds: String(s.max_video_seconds.value),
    max_upload_mb: String(s.max_upload_mb.value),
    max_friends: String(s.max_friends.value),
  };
}

const REG_OPTIONS: { value: Registration; title: string; text: string }[] = [
  { value: "invite", title: "Invite only", text: "People need an invite link to create an account. Recommended." },
  { value: "open", title: "Open", text: "Anyone who knows this server's address can create an account." },
  { value: "closed", title: "Closed", text: "Nobody can sign up. Admins create accounts under Users." },
];

const LIMITS: { key: "max_video_seconds" | "max_upload_mb" | "max_friends"; label: string; hint: string; min: number; max: number; unit: string }[] = [
  { key: "max_video_seconds", label: "Max video length", hint: "1–600 seconds.", min: 1, max: 600, unit: "s" },
  { key: "max_upload_mb", label: "Max upload size", hint: "Per photo or video, 1–1024 MB.", min: 1, max: 1024, unit: "MB" },
  { key: "max_friends", label: "Max friends", hint: "Per person. 0 = unlimited.", min: 0, max: 1_000_000, unit: "" },
];

function SourceNote({ s, onReset, pending }: { s: SettingValue<unknown>; onReset: () => void; pending: boolean }) {
  if (s.locked) {
    return (
      <Badge variant="muted" className="w-fit justify-self-start" title="This value comes from frame.conf or the environment. Change it there and restart.">
        <Lock className="h-3 w-3" aria-hidden="true" />
        Set in frame.conf
      </Badge>
    );
  }
  if (s.source === "panel") {
    return (
      <Button variant="link" size="sm" className="h-auto w-fit justify-self-start p-0 text-xs" onClick={onReset} disabled={pending}>
        <RotateCcw aria-hidden="true" />
        Reset to default ({String(s.default)})
      </Button>
    );
  }
  return null;
}

export function SettingsPage() {
  const { refresh } = useSession();
  const toast = useToast();
  const res = useResource((signal) => get<{ settings: Settings }>("settings", { signal }), []);
  const [draft, setDraft] = React.useState<Draft | null>(null);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);

  const settings = res.data?.settings;
  React.useEffect(() => {
    if (settings) setDraft(toDraft(settings));
  }, [settings]);

  if (!settings || !draft) {
    return (
      <>
        <PageHeader title="Settings" />
        {res.error ? <ErrorBox error={res.error} onRetry={res.reload} /> : <Spinner />}
      </>
    );
  }

  const original = toDraft(settings);
  const set = (p: Partial<Draft>) => setDraft({ ...draft, ...p });
  const anyLocked = (Object.keys(settings) as SettingKey[]).some((k) => settings[k].locked);

  const numErrors: Partial<Record<string, string>> = {};
  for (const l of LIMITS) {
    const n = Number(draft[l.key]);
    if (!Number.isInteger(n) || n < l.min || n > l.max) numErrors[l.key] = `Enter a whole number from ${l.min} to ${l.max}`;
  }
  const nameError = draft.name.trim().length < 1 || new TextEncoder().encode(draft.name.trim()).length > 64 ? "Use 1–64 characters" : null;

  const changes: Record<string, unknown> = {};
  if (!settings.registration.locked && draft.registration !== original.registration) changes.registration = draft.registration;
  if (!settings.user_invites.locked && draft.user_invites !== original.user_invites) changes.user_invites = draft.user_invites;
  if (!settings.name.locked && draft.name.trim() !== original.name) changes.name = draft.name.trim();
  for (const l of LIMITS) {
    if (!settings[l.key].locked && draft[l.key] !== original[l.key]) changes[l.key] = Number(draft[l.key]);
  }
  const dirty = Object.keys(changes).length > 0;
  const invalid = !!nameError || Object.keys(numErrors).length > 0;

  async function save(body: Record<string, unknown>, message: string) {
    setPending(true);
    setError(null);
    try {
      const r = await patch<{ settings: Settings }>("settings", body);
      res.setData(r);
      toast(message);
      void refresh(); // server name in the sidebar
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <>
      <PageHeader
        title="Settings"
        description="Changes apply immediately, without a restart."
      />
      {anyLocked ? (
        <p className="mb-6 flex max-w-2xl items-start gap-2 text-sm text-muted-foreground">
          <Lock className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          Settings marked “Set in frame.conf” come from the config file or environment. They win over the panel; change
          them there and restart the server.
        </p>
      ) : null}
      <form
        className="grid gap-6"
        onSubmit={(e) => {
          e.preventDefault();
          if (dirty && !invalid) void save(changes, "Settings saved.");
        }}
      >
        <Card>
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <CardTitle>Who can join</CardTitle>
              <SourceNote s={settings.registration} pending={pending} onReset={() => save({ registration: null }, "Registration reset to the default.")} />
            </div>
            <CardDescription>Signing in to an existing account never needs an invite.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-5">
            <fieldset disabled={settings.registration.locked} className="grid gap-3 disabled:opacity-70">
              <legend className="sr-only">Registration</legend>
              {REG_OPTIONS.map((o) => (
                <label
                  key={o.value}
                  className={cn(
                    "flex cursor-pointer items-start gap-3 rounded-md border-2 p-3 focus-within:ring-2 focus-within:ring-ring",
                    draft.registration === o.value ? "border-primary bg-soft" : "border-border",
                    settings.registration.locked && "cursor-not-allowed",
                  )}
                >
                  <input
                    type="radio"
                    name="registration"
                    value={o.value}
                    checked={draft.registration === o.value}
                    onChange={() => set({ registration: o.value })}
                    className="mt-1 h-4 w-4 accent-[hsl(var(--primary))]"
                  />
                  <span>
                    <span className="block font-azeret text-sm font-bold">{o.title}</span>
                    <span className="block text-sm text-muted-foreground">{o.text}</span>
                  </span>
                </label>
              ))}
            </fieldset>
            <div className="flex items-start justify-between gap-4 rounded-md border-2 border-border p-3">
              <div>
                <Label htmlFor="user-invites" className="normal-case tracking-normal text-sm">
                  Members can invite
                </Label>
                <p id="user-invites-hint" className="mt-1 text-sm text-muted-foreground">
                  On invite-only servers, members' invite links from the app can also bring in new people (who become
                  their friend). Off: only invites from this panel let people join.
                </p>
                <div className="mt-2">
                  <SourceNote s={settings.user_invites} pending={pending} onReset={() => save({ user_invites: null }, "Reset to the default.")} />
                </div>
              </div>
              <Switch
                id="user-invites"
                aria-describedby="user-invites-hint"
                checked={draft.user_invites}
                disabled={settings.user_invites.locked}
                onCheckedChange={(v) => set({ user_invites: v })}
              />
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Server</CardTitle>
            <CardDescription>Shown in the app when people connect.</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Label htmlFor="server-name">Server name</Label>
                <SourceNote s={settings.name} pending={pending} onReset={() => save({ name: null }, "Name reset to the default.")} />
              </div>
              <Input
                id="server-name"
                maxLength={64}
                disabled={settings.name.locked}
                aria-invalid={!!nameError}
                value={draft.name}
                onChange={(e) => set({ name: e.target.value })}
              />
              {nameError ? <p className="text-xs font-medium text-destructive">{nameError}</p> : null}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Limits</CardTitle>
            <CardDescription>The apps read these from the server.</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-5 md:grid-cols-3">
            {LIMITS.map((l) => (
              <div key={l.key} className="grid content-start gap-2">
                <Label htmlFor={`limit-${l.key}`}>{l.label}</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id={`limit-${l.key}`}
                    type="number"
                    inputMode="numeric"
                    min={l.min}
                    max={l.max}
                    disabled={settings[l.key].locked}
                    aria-invalid={!!numErrors[l.key]}
                    aria-describedby={`limit-${l.key}-hint`}
                    value={draft[l.key]}
                    onChange={(e) => set({ [l.key]: e.target.value } as Partial<Draft>)}
                    className="font-azeret"
                  />
                  {l.unit ? <span className="font-azeret text-sm text-muted-foreground">{l.unit}</span> : null}
                </div>
                <p id={`limit-${l.key}-hint`} className="text-xs text-muted-foreground">
                  {l.hint}
                </p>
                {numErrors[l.key] ? <p className="text-xs font-medium text-destructive">{numErrors[l.key]}</p> : null}
                <SourceNote
                  s={settings[l.key]}
                  pending={pending}
                  onReset={() => save({ [l.key]: null }, `${l.label} reset to the default.`)}
                />
              </div>
            ))}
          </CardContent>
        </Card>

        <FormError error={error} />
        <div className="flex flex-wrap gap-3">
          <Button type="submit" size="lg" disabled={!dirty || invalid || pending}>
            {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
            Save changes
          </Button>
          <Button type="button" variant="outline" size="lg" disabled={!dirty || pending} onClick={() => setDraft(original)}>
            Discard
          </Button>
        </div>
      </form>
    </>
  );
}
