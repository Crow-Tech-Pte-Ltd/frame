import * as React from "react";
import { ArrowRight, CircleCheck, CircleHelp, CircleX, EyeOff, Flag } from "lucide-react";

import { get } from "@/lib/api";
import { describe } from "@/lib/audit";
import { bytes, duration, num, plural } from "@/lib/format";
import { Link } from "@/lib/router";
import type { Overview } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Avatar, EmptyState, ErrorBox, PageHeader, PRIVACY_NOTE, Spinner, Stat, Time, useResource } from "@/components/common";

export const REGISTRATION_LABEL: Record<string, string> = {
  invite: "Invite only",
  open: "Open",
  closed: "Closed",
};

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5 border-b border-border py-2.5 last:border-b-0 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
      <dt className="font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-sm sm:text-right">{children}</dd>
    </div>
  );
}

function PushStatus({ push }: { push: Overview["push"] }) {
  if (!push.configured) return <Badge variant="muted">Off</Badge>;
  if (push.reachable === null)
    return (
      <Badge variant="muted">
        <CircleHelp className="h-3 w-3" aria-hidden="true" /> Checking…
      </Badge>
    );
  return push.reachable ? (
    <Badge variant="success">
      <CircleCheck className="h-3 w-3" aria-hidden="true" /> Reachable
    </Badge>
  ) : (
    <Badge variant="danger">
      <CircleX className="h-3 w-3" aria-hidden="true" /> Unreachable
    </Badge>
  );
}

export function OverviewPage() {
  const { data, error, loading, reload } = useResource((signal) => get<Overview>("overview", { signal }), []);
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(t);
  }, []);

  if (!data) {
    return (
      <>
        <PageHeader title="Overview" />
        {error ? <ErrorBox error={error} onRetry={reload} /> : loading ? <Spinner /> : null}
      </>
    );
  }
  const s = data.server;

  return (
    <>
      <PageHeader
        title="Overview"
        description={
          <>
            <span className="font-bold text-foreground">{s.name}</span> · Frame {s.version} · up {duration(now - s.started_at)}
          </>
        }
        actions={
          <>
            <Link to="/reports" className="rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <Badge variant={data.reports.open > 0 ? "warning" : "muted"} className="text-sm">
                <Flag className="h-3 w-3" aria-hidden="true" />
                Open reports: {num(data.reports.open)}
              </Badge>
            </Link>
            <Badge variant="kuning" className="text-sm">
              Registration: {REGISTRATION_LABEL[s.registration] ?? s.registration}
            </Badge>
          </>
        }
      />

      {data.reports.open > 0 ? (
        <Alert variant="warning" className="mb-6">
          <Flag className="h-4 w-4" />
          <AlertTitle>{plural(data.reports.open, "open report")}</AlertTitle>
          <AlertDescription>
            Members reported content or people for you to review.{" "}
            <Link to="/reports?status=open" className="font-bold underline underline-offset-2">
              Review reports
            </Link>
            .
          </AlertDescription>
        </Alert>
      ) : null}

      <p className="mb-6 flex items-center gap-2 text-sm text-muted-foreground">
        <EyeOff className="h-4 w-4 shrink-0" aria-hidden="true" />
        {PRIVACY_NOTE}
      </p>

      <section aria-labelledby="stats-people" className="mb-8">
        <h2 id="stats-people" className="sr-only">
          People and content
        </h2>
        <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
          <Stat label="Users" value={num(data.users.total)} />
          <Stat label="Active · 7 days" value={num(data.users.active_7d)} />
          <Stat label="Suspended" value={num(data.users.disabled)} />
          <Stat label="Admins" value={num(data.users.admins)} />
          <Stat label="Moments" value={num(data.content.moments)} />
          <Stat label="Messages" value={num(data.content.messages)} />
          <Stat label="Media" value={bytes(data.storage.media_bytes)} hint={plural(data.storage.media_files, "file")} />
          <Stat label="Database" value={bytes(data.database.bytes)} />
        </div>
      </section>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Server</CardTitle>
          </CardHeader>
          <CardContent>
            <dl>
              <Row label="Registration">
                {REGISTRATION_LABEL[s.registration] ?? s.registration}
                {s.registration === "invite" ? (
                  <span className="text-muted-foreground">
                    {" "}
                    · {s.user_invites ? "members can invite" : "admin invites only"}
                  </span>
                ) : null}
              </Row>
              <Row label="Public address">
                {s.public_url ? (
                  <span className="font-azeret text-xs">{s.public_url}</span>
                ) : (
                  <span className="text-muted-foreground">Not set (FRAME_PUBLIC_URL)</span>
                )}
              </Row>
              <Row label="Media storage">{s.storage === "s3" ? "S3 bucket" : "Local disk"}</Row>
              <Row label="Admin panel">
                {s.admin_mode === "local" ? "Local networks only" : "On"}
                {s.admin_listen ? <span className="text-muted-foreground"> · separate address</span> : null}
              </Row>
              <Row label="Push relay">
                <span className="inline-flex flex-wrap items-center justify-end gap-2">
                  <PushStatus push={data.push} />
                  {data.push.relay_url ? <span className="font-azeret text-xs">{data.push.relay_url}</span> : null}
                </span>
              </Row>
              {data.push.checked_at ? (
                <Row label="Relay checked">
                  <Time ms={data.push.checked_at} />
                </Row>
              ) : null}
            </dl>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle>Recent sign-ups</CardTitle>
            <Link to="/users" className="inline-flex items-center gap-1 font-azeret text-xs font-bold text-biru-400 hover:underline">
              All users <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          </CardHeader>
          <CardContent>
            {data.recent_signups.length === 0 ? (
              <EmptyState>No one has joined yet. Create an invite to get started.</EmptyState>
            ) : (
              <ul className="grid gap-1">
                {data.recent_signups.map((u) => (
                  <li key={u.id}>
                    <Link
                      to={`/users/${u.id}`}
                      className="flex items-center gap-3 rounded-md px-2 py-2 hover:bg-soft focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <Avatar user={u} size="sm" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-bold">{u.display_name}</span>
                        <span className="block truncate font-azeret text-xs text-muted-foreground">
                          {u.username ? "@" + u.username : "no username yet"}
                        </span>
                      </span>
                      <Time ms={u.created_at} className="shrink-0 text-xs text-muted-foreground" />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>

        <Card className="lg:col-span-2">
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle>Recent admin actions</CardTitle>
            <Link to="/audit" className="inline-flex items-center gap-1 font-azeret text-xs font-bold text-biru-400 hover:underline">
              Audit log <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          </CardHeader>
          <CardContent>
            {data.recent_actions.length === 0 ? (
              <EmptyState>Nothing yet.</EmptyState>
            ) : (
              <ul className="divide-y divide-border">
                {data.recent_actions.map((a) => (
                  <li key={a.id} className="flex flex-col gap-0.5 py-2.5 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
                    <span className="text-sm">{describe(a)}</span>
                    <Time ms={a.at} className="shrink-0 text-xs text-muted-foreground" />
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      </div>
    </>
  );
}
