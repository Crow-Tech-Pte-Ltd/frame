import { get } from "@/lib/api";
import { actionVerb } from "@/lib/audit";
import { setSearch, useSearchParams } from "@/lib/router";
import type { AuditEntry, Paged } from "@/lib/types";
import { EmptyState, ErrorBox, PageHeader, Pagination, Spinner, Time, useResource } from "@/components/common";

const PER_PAGE = 50;

function Target({ e }: { e: AuditEntry }) {
  if (!e.target) return <span className="text-muted-foreground">—</span>;
  return (
    <span>
      {e.target.label}
      <span className="ml-1 font-azeret text-[10px] uppercase text-muted-foreground">{e.target.type}</span>
    </span>
  );
}

export function AuditPage() {
  const params = useSearchParams();
  const page = Math.max(1, Number(params.get("page") ?? "1") || 1);
  const res = useResource(
    (signal) => get<Paged<"entries", AuditEntry>>(`audit?page=${page}&per_page=${PER_PAGE}`, { signal }),
    [page],
  );
  return (
    <>
      <PageHeader
        title="Audit log"
        description="Every admin action and panel sign-in: who did what, to whom, when and from where. Entries never contain passwords, codes or tokens."
      />
      {res.error && !res.data ? <ErrorBox error={res.error} onRetry={res.reload} /> : null}
      {!res.data && res.loading ? <Spinner /> : null}
      {res.data ? (
        <div aria-busy={res.loading}>
          {res.data.entries.length === 0 ? (
            <EmptyState>No entries yet.</EmptyState>
          ) : (
            <>
              <div className="hidden overflow-x-auto rounded-lg border-2 border-primary bg-card shadow-brutal md:block">
                <table className="w-full text-left text-sm">
                  <caption className="sr-only">Audit log</caption>
                  <thead className="border-b-2 border-primary bg-kuning-100 font-azeret text-xs uppercase tracking-wide text-primary-400">
                    <tr>
                      <th scope="col" className="px-4 py-3">When</th>
                      <th scope="col" className="px-4 py-3">Who</th>
                      <th scope="col" className="px-4 py-3">Action</th>
                      <th scope="col" className="px-4 py-3">Target</th>
                      <th scope="col" className="px-4 py-3">Details</th>
                      <th scope="col" className="px-4 py-3">IP</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {res.data.entries.map((e) => (
                      <tr key={e.id} className="align-top">
                        <td className="whitespace-nowrap px-4 py-3">
                          <Time ms={e.at} />
                        </td>
                        <td className="px-4 py-3 font-bold">{e.actor.label}</td>
                        <td className="px-4 py-3">
                          <span className="block">{actionVerb(e.action)}</span>
                          <code className="font-azeret text-[11px] text-muted-foreground">{e.action}</code>
                        </td>
                        <td className="px-4 py-3">
                          <Target e={e} />
                        </td>
                        <td className="max-w-[16rem] break-words px-4 py-3 text-xs">{e.detail ?? <span className="text-muted-foreground">—</span>}</td>
                        <td className="whitespace-nowrap px-4 py-3 font-azeret text-xs">{e.ip}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <ul className="grid gap-3 md:hidden">
                {res.data.entries.map((e) => (
                  <li key={e.id} className="rounded-lg border-2 border-primary bg-card p-4 shadow-brutal-sm">
                    <p className="text-sm">
                      <span className="font-bold">{e.actor.label}</span> {actionVerb(e.action)}
                      {e.target ? <> {e.target.label}</> : null}
                    </p>
                    {e.detail ? <p className="mt-1 break-words text-xs">{e.detail}</p> : null}
                    <p className="mt-2 text-xs text-muted-foreground">
                      <Time ms={e.at} /> · <span className="font-azeret">{e.ip}</span>
                    </p>
                  </li>
                ))}
              </ul>
            </>
          )}
          <Pagination
            page={res.data.page}
            perPage={res.data.per_page}
            total={res.data.total}
            onPage={(p) => setSearch({ page: p === 1 ? null : String(p) }, false)}
          />
        </div>
      ) : null}
    </>
  );
}
