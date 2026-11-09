// Reports from members (docs/ADMIN.md "Reports"). The list shows metadata
// only; the drawer shows exactly the one reported item, which a member chose
// to share with the admin by reporting it.
import * as React from "react";
import { Ban, Eye, Flag, MessageSquare, Trash2, UserRound, X } from "lucide-react";

import { useAdmin, useToast } from "@/app-context";
import { get, post } from "@/lib/api";
import { userLabel } from "@/lib/format";
import { REASON_LABEL, RESOLUTION_LABEL, TARGET_LABEL, reportsChanged } from "@/lib/reports";
import { Link, navigate, setSearch, useLocation, useSearchParams } from "@/lib/router";
import type { AdminReport, ReportContent, ReportList, ReportUser } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { AlertDialog, AlertDialogContent } from "@/components/ui/alert-dialog";
import { EmptyState, ErrorBox, PageHeader, Pagination, Select, Spinner, Time, useResource } from "@/components/common";
import { ConfirmDialog } from "@/components/dialogs";

const PER_PAGE = 25;

function Person({ u, fallback = "Deleted account" }: { u: ReportUser | null; fallback?: string }) {
  if (!u) return <span className="text-muted-foreground">{fallback}</span>;
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1">
      <Link
        to={`/users/${encodeURIComponent(u.id)}`}
        className="truncate font-bold underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {userLabel(u)}
      </Link>
      {u.suspended ? <Badge variant="danger">Suspended</Badge> : null}
    </span>
  );
}

function StatusBadge({ r }: { r: AdminReport }) {
  if (r.status === "open") return <Badge variant="warning">Open</Badge>;
  return <Badge variant="muted">{r.resolution ? RESOLUTION_LABEL[r.resolution] : "Resolved"}</Badge>;
}

function TargetBadge({ r }: { r: AdminReport }) {
  return (
    <span className="inline-flex items-center gap-1 font-azeret text-xs">
      {TARGET_LABEL[r.target_type]}
      {!r.content_exists ? <span className="text-muted-foreground">(gone)</span> : null}
    </span>
  );
}

export function ReportsPage({ reportID }: { reportID: string | null }) {
  const params = useSearchParams();
  const { search } = useLocation();
  const status = params.get("status") ?? "all";
  const page = Math.max(1, Number(params.get("page") ?? "1") || 1);
  const list = useResource(
    (signal) =>
      get<ReportList>(`reports?status=${encodeURIComponent(status)}&page=${page}&per_page=${PER_PAGE}`, { signal }),
    [status, page],
  );
  const toReport = (id: string) => `/reports/${encodeURIComponent(id)}${search}`;

  return (
    <>
      <PageHeader
        title="Reports"
        description="Moments, messages and profiles that members reported to you. Nothing is scanned automatically: every report was sent by a person."
        actions={
          list.data ? (
            <Badge variant={list.data.open > 0 ? "warning" : "muted"} className="text-sm">
              {list.data.open} open
            </Badge>
          ) : null
        }
      />

      <div className="mb-4 grid gap-3 rounded-lg border-2 border-primary bg-page p-3 sm:grid-cols-[12rem] sm:items-end">
        <div className="grid gap-2">
          <Label htmlFor="reports-status">Show</Label>
          <Select
            id="reports-status"
            value={status}
            onChange={(e) => setSearch({ status: e.target.value === "all" ? null : e.target.value, page: null })}
          >
            <option value="all">All (open first)</option>
            <option value="open">Open</option>
            <option value="resolved">Resolved</option>
          </Select>
        </div>
      </div>

      {list.error && !list.data ? <ErrorBox error={list.error} onRetry={list.reload} /> : null}
      {!list.data && list.loading ? <Spinner /> : null}
      {list.data ? (
        <div aria-busy={list.loading}>
          {list.data.reports.length === 0 ? (
            <EmptyState>{status === "all" ? "No reports. When a member reports something, it shows up here." : "No reports match."}</EmptyState>
          ) : (
            <>
              <div className="hidden overflow-x-auto rounded-lg border-2 border-primary bg-card shadow-brutal md:block">
                <table className="w-full text-left text-sm">
                  <caption className="sr-only">Reports</caption>
                  <thead className="border-b-2 border-primary bg-kuning-100 font-azeret text-xs uppercase tracking-wide text-primary-400">
                    <tr>
                      <th scope="col" className="px-4 py-3">Status</th>
                      <th scope="col" className="px-4 py-3">Reported user</th>
                      <th scope="col" className="px-4 py-3">Type</th>
                      <th scope="col" className="px-4 py-3">Reason</th>
                      <th scope="col" className="px-4 py-3">Reporter</th>
                      <th scope="col" className="px-4 py-3">Note</th>
                      <th scope="col" className="px-4 py-3">Date</th>
                      <th scope="col" className="px-4 py-3">
                        <span className="sr-only">Open</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {list.data.reports.map((r) => (
                      <tr key={r.id} className={cn("hover:bg-soft", r.status !== "open" && "opacity-75")}>
                        <td className="whitespace-nowrap px-4 py-3">
                          <StatusBadge r={r} />
                        </td>
                        <td className="max-w-[12rem] px-4 py-3">
                          <Person u={r.reported_user} />
                        </td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <TargetBadge r={r} />
                        </td>
                        <td className="px-4 py-3">{REASON_LABEL[r.reason] ?? r.reason}</td>
                        <td className="max-w-[10rem] px-4 py-3">
                          <Person u={r.reporter} />
                        </td>
                        <td className="max-w-[16rem] px-4 py-3">
                          {r.note ? <span className="line-clamp-2 break-words">{r.note}</span> : <span className="text-muted-foreground">—</span>}
                        </td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <Time ms={r.created_at} />
                        </td>
                        <td className="px-4 py-3 text-right">
                          <Button variant="outline" size="sm" onClick={() => navigate(toReport(r.id))}>
                            Review
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <ul className="grid gap-3 md:hidden">
                {list.data.reports.map((r) => (
                  <li key={r.id}>
                    <Link
                      to={toReport(r.id)}
                      className={cn(
                        "block rounded-lg border-2 border-primary bg-card p-4 shadow-brutal-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                        r.status !== "open" && "opacity-75",
                      )}
                    >
                      <div className="flex items-center justify-between gap-3">
                        <span className="font-bold">{REASON_LABEL[r.reason] ?? r.reason}</span>
                        <StatusBadge r={r} />
                      </div>
                      <p className="mt-2 text-sm">
                        <TargetBadge r={r} /> by{" "}
                        <span className="font-bold">{r.reported_user ? userLabel(r.reported_user) : "a deleted account"}</span>
                      </p>
                      {r.note ? <p className="mt-2 line-clamp-3 break-words text-sm text-muted-foreground">“{r.note}”</p> : null}
                      <p className="mt-2 text-xs text-muted-foreground">
                        Reported by {r.reporter ? userLabel(r.reporter) : "a deleted account"} · <Time ms={r.created_at} />
                      </p>
                    </Link>
                  </li>
                ))}
              </ul>
            </>
          )}
          <Pagination
            page={list.data.page}
            perPage={list.data.per_page}
            total={list.data.total}
            onPage={(p) => setSearch({ page: p === 1 ? null : String(p) }, false)}
          />
        </div>
      ) : null}

      <AlertDialog open={reportID !== null} onOpenChange={(o) => !o && navigate("/reports" + search)}>
        <AlertDialogContent layout="drawer" dismissOnBackdrop>
          {reportID ? (
            <ReportBody
              reportID={reportID}
              onClose={() => navigate("/reports" + search)}
              onChanged={() => {
                list.reload();
                reportsChanged();
              }}
            />
          ) : null}
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-3 border-b border-border py-2 text-sm last:border-b-0">
      <dt className="font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="border-t-2 border-primary px-5 py-5 sm:px-6">
      <h3 className="mb-3 font-azeret text-sm font-bold uppercase tracking-wide">{title}</h3>
      {children}
    </section>
  );
}

function ContentView({ content }: { content: ReportContent }) {
  if (content.type === "moment") {
    const m = content.media;
    const browserImage = m && (m.content_type === "image/jpeg" || m.content_type === "image/png");
    return (
      <figure className="grid gap-3">
        <div className="mx-auto w-full max-w-xs overflow-hidden rounded-lg border-2 border-primary bg-muted">
          {m && m.content_type === "video/mp4" ? (
            <video src={m.url} poster={content.thumb.url} controls playsInline preload="metadata" className="aspect-square w-full bg-black object-contain">
              <a href={m.url}>Download the video</a>
            </video>
          ) : (
            <img
              src={browserImage ? m.url : content.thumb.url}
              alt="The reported moment"
              className="aspect-square w-full object-contain"
            />
          )}
        </div>
        <figcaption className="grid gap-1 text-sm">
          {content.caption ? (
            <p>
              <span className="font-azeret text-xs font-bold uppercase tracking-wide text-muted-foreground">Caption </span>
              <span className="break-words">{content.caption.text}</span>
            </p>
          ) : (
            <p className="text-muted-foreground">No caption.</p>
          )}
          <p className="text-xs text-muted-foreground">
            {content.kind === "video" ? "Video" : "Photo"} posted <Time ms={content.created_at} mode="absolute" />
            {m && !browserImage && m.content_type !== "video/mp4" ? " · the full photo is HEIC, so the thumbnail is shown" : ""}
          </p>
        </figcaption>
      </figure>
    );
  }
  if (content.type === "message") {
    return (
      <figure className="grid gap-2">
        <blockquote className="whitespace-pre-wrap break-words rounded-lg border-2 border-primary bg-soft px-4 py-3 text-sm text-soft-foreground">
          {content.text}
        </blockquote>
        <figcaption className="text-xs text-muted-foreground">
          Sent <Time ms={content.created_at} mode="absolute" />
        </figcaption>
      </figure>
    );
  }
  return (
    <p className="text-sm">
      The report is about the profile of <Person u={content.user} />, who joined <Time ms={content.created_at} />. Open their
      account for details.
    </p>
  );
}

type Pending = null | "delete" | "suspend" | "dismiss";

function ReportBody({ reportID, onClose, onChanged }: { reportID: string; onClose: () => void; onChanged: () => void }) {
  const me = useAdmin().admin;
  const toast = useToast();
  const res = useResource(
    (signal) => get<{ report: AdminReport; content: ReportContent | null }>(`reports/${encodeURIComponent(reportID)}`, { signal }),
    [reportID],
  );
  const [pending, setPending] = React.useState<Pending>(null);

  const header = (
    <div className="sticky top-0 z-10 flex items-center justify-between gap-3 border-b-2 border-primary bg-page px-5 py-4 sm:px-6">
      <h2 className="font-azeret text-lg font-bold">Report</h2>
      <Button variant="outline" size="icon" onClick={onClose} aria-label="Close">
        <X aria-hidden="true" />
      </Button>
    </div>
  );
  const d = res.data;
  if (!d) {
    return (
      <div className="flex h-full flex-col">
        {header}
        <div className="p-6">{res.error ? <ErrorBox error={res.error} onRetry={res.reload} /> : <Spinner />}</div>
      </div>
    );
  }
  const r = d.report;
  const owner = r.reported_user;
  const self = owner?.id === me.id;
  const canDelete = r.target_type !== "user" && d.content !== null;
  const what = r.target_type === "moment" ? "moment" : r.target_type === "message" ? "message" : "profile";
  const done = (msg: string) => {
    toast(msg);
    res.reload();
    onChanged();
  };
  const resolvedMsg = (n: number) => (n > 1 ? ` ${n} reports on it are resolved.` : "");

  return (
    <div className="flex min-h-full flex-col">
      {header}
      <div className="px-5 py-5 sm:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge r={r} />
          <Badge variant="outline">{TARGET_LABEL[r.target_type]}</Badge>
        </div>
        <p className="mt-3 font-azeret text-xl font-bold">{REASON_LABEL[r.reason] ?? r.reason}</p>
        <dl className="mt-4">
          <Fact label="Reported user">
            <Person u={owner} />
          </Fact>
          <Fact label="Reporter">
            <Person u={r.reporter} />
          </Fact>
          <Fact label="Reported">
            <Time ms={r.created_at} mode="absolute" />
          </Fact>
          <Fact label="Note">{r.note ? <span className="whitespace-pre-wrap">{r.note}</span> : <span className="text-muted-foreground">—</span>}</Fact>
          {r.status === "resolved" ? (
            <Fact label="Resolved">
              {r.resolution ? RESOLUTION_LABEL[r.resolution] : "Resolved"}
              {r.resolved_by ? <> by {userLabel(r.resolved_by)}</> : null}
              {r.resolved_at ? (
                <>
                  {" "}
                  · <Time ms={r.resolved_at} />
                </>
              ) : null}
            </Fact>
          ) : null}
        </dl>
      </div>

      <Section title={`Reported ${what}`}>
        <p className="mb-3 flex items-start gap-2 text-xs text-muted-foreground">
          <Eye className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          Shown because a member reported it to you. Only this item is shown, and opening it is recorded in the audit log.
        </p>
        {d.content ? (
          <ContentView content={d.content} />
        ) : (
          <EmptyState>
            This {what} no longer exists
            {r.resolution === "deleted_content" ? " (deleted from this report)." : " (deleted by its author or with the account)."}
          </EmptyState>
        )}
      </Section>

      <Section title="Actions">
        {self ? <p className="mb-3 text-sm text-muted-foreground">This report is about you, so you can't suspend yourself here.</p> : null}
        <div className="grid gap-2 sm:grid-cols-2">
          {r.target_type !== "user" ? (
            <Button variant="danger-outline" onClick={() => setPending("delete")} disabled={!canDelete}>
              {r.target_type === "message" ? <MessageSquare aria-hidden="true" /> : <Trash2 aria-hidden="true" />}
              Delete {what}
            </Button>
          ) : null}
          <Button variant="outline" onClick={() => setPending("suspend")} disabled={!owner || self || owner.suspended}>
            <Ban aria-hidden="true" />
            {owner?.suspended ? "User suspended" : "Suspend user"}
          </Button>
          {r.status === "open" ? (
            <Button variant="outline" onClick={() => setPending("dismiss")}>
              <Flag aria-hidden="true" />
              Dismiss
            </Button>
          ) : null}
          {owner ? (
            <Button variant="ghost" onClick={() => navigate(`/users/${encodeURIComponent(owner.id)}`)}>
              <UserRound aria-hidden="true" />
              Open account
            </Button>
          ) : null}
        </div>
      </Section>

      <ConfirmDialog
        open={pending === "delete"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Delete this ${what}?`}
        description={`It disappears for everyone${r.target_type === "moment" ? ", with its photo or video" : ""}, as if ${owner ? userLabel(owner) : "its author"} had deleted it. It can't be undone (except from an older backup).`}
        confirmLabel={`Delete ${what}`}
        destructive
        onConfirm={async () => {
          const out = await post<{ resolved: number }>(`reports/${encodeURIComponent(r.id)}/delete-content`);
          done(`The ${what} was deleted.${resolvedMsg(out.resolved)}`);
        }}
      />
      <ConfirmDialog
        open={pending === "suspend"}
        onOpenChange={(o) => !o && setPending(null)}
        title={`Suspend ${owner ? userLabel(owner) : "this user"}?`}
        description="They're signed out everywhere, can't sign in, and disappear from search and contact matching. Their moments and messages stay. You can unsuspend them from Users at any time."
        confirmLabel="Suspend"
        destructive
        onConfirm={async () => {
          const out = await post<{ resolved: number }>(`reports/${encodeURIComponent(r.id)}/suspend-user`);
          done(`${owner ? userLabel(owner) : "The user"} is suspended.${resolvedMsg(out.resolved)}`);
        }}
      />
      <ConfirmDialog
        open={pending === "dismiss"}
        onOpenChange={(o) => !o && setPending(null)}
        title="Dismiss this report?"
        description={`Nothing happens to the ${what} or its author. Other open reports on the same ${what} are dismissed too.`}
        confirmLabel="Dismiss"
        onConfirm={async () => {
          const out = await post<{ resolved: number }>(`reports/${encodeURIComponent(r.id)}/dismiss`);
          done(`Report dismissed.${resolvedMsg(out.resolved)}`);
        }}
      />
    </div>
  );
}
