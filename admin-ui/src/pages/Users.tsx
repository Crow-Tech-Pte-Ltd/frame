import * as React from "react";
import { EyeOff, Loader2, Search, UserPlus } from "lucide-react";

import { useToast } from "@/app-context";
import { get, post } from "@/lib/api";
import { bytes, num } from "@/lib/format";
import { Link, navigate, setSearch, useLocation, useSearchParams } from "@/lib/router";
import type { AdminUser, Paged } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Avatar,
  CopyField,
  EmptyState,
  ErrorBox,
  FormError,
  PageHeader,
  Pagination,
  PRIVACY_NOTE,
  Select,
  Spinner,
  Time,
  useResource,
} from "@/components/common";
import { emptyIdentity, IdentityFields, identityErrors, type Identity } from "@/components/identity-fields";
import { UserDrawer } from "@/pages/UserDrawer";
import { UserBadges } from "@/components/user-badges";

const SORTS: { value: string; label: string; sort: string; order: "asc" | "desc" }[] = [
  { value: "newest", label: "Newest first", sort: "created_at", order: "desc" },
  { value: "oldest", label: "Oldest first", sort: "created_at", order: "asc" },
  { value: "seen", label: "Recently active", sort: "last_seen_at", order: "desc" },
  { value: "name", label: "Name A–Z", sort: "display_name", order: "asc" },
  { value: "moments", label: "Most moments", sort: "moments", order: "desc" },
  { value: "storage", label: "Most storage", sort: "storage_bytes", order: "desc" },
  { value: "friends", label: "Most friends", sort: "friends", order: "desc" },
];

const PER_PAGE = 25;

function contact(u: AdminUser) {
  const parts = [u.email_masked, u.phone_masked].filter(Boolean) as string[];
  if (parts.length === 0) {
    const via = u.auth_methods.filter((m) => m !== "password");
    return <span className="text-muted-foreground">{via.length ? `${via.join(", ")} sign-in` : "—"}</span>;
  }
  return (
    <span className="grid font-azeret text-xs">
      {parts.map((p) => (
        <span key={p} className="truncate">
          {p}
        </span>
      ))}
    </span>
  );
}

export function UsersPage({ userID }: { userID: string | null }) {
  const params = useSearchParams();
  const { search } = useLocation();
  const q = params.get("q") ?? "";
  const status = params.get("status") ?? "all";
  const sortKey = params.get("sort") ?? "newest";
  const page = Math.max(1, Number(params.get("page") ?? "1") || 1);
  const sort = SORTS.find((s) => s.value === sortKey) ?? SORTS[0];

  const [query, setQuery] = React.useState(q);
  React.useEffect(() => setQuery(q), [q]);
  React.useEffect(() => {
    if (query === q) return;
    const t = window.setTimeout(() => setSearch({ q: query.trim() || null, page: null }), 300);
    return () => window.clearTimeout(t);
  }, [query, q]);

  const list = useResource(
    (signal) => {
      const p = new URLSearchParams({
        status,
        sort: sort.sort,
        order: sort.order,
        page: String(page),
        per_page: String(PER_PAGE),
      });
      if (q) p.set("q", q);
      return get<Paged<"users", AdminUser>>("users?" + p.toString(), { signal });
    },
    [q, status, sort.sort, sort.order, page],
  );

  const [newOpen, setNewOpen] = React.useState(false);
  const toUser = (id: string) => `/users/${encodeURIComponent(id)}${search}`;

  return (
    <>
      <PageHeader
        title="Users"
        description={
          <span className="inline-flex items-center gap-2">
            <EyeOff className="h-4 w-4 shrink-0" aria-hidden="true" />
            {PRIVACY_NOTE}
          </span>
        }
        actions={
          <Button onClick={() => setNewOpen(true)}>
            <UserPlus aria-hidden="true" />
            New user
          </Button>
        }
      />

      <div className="mb-4 grid gap-3 rounded-lg border-2 border-primary bg-page p-3 sm:grid-cols-[minmax(0,1fr)_11rem_12rem] sm:items-end">
        <div className="grid gap-2">
          <Label htmlFor="users-q">Search</Label>
          <div className="relative">
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
            <Input
              id="users-q"
              type="search"
              placeholder="Name, username, email or phone"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-9"
            />
          </div>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="users-status">Show</Label>
          <Select id="users-status" value={status} onChange={(e) => setSearch({ status: e.target.value === "all" ? null : e.target.value, page: null })}>
            <option value="all">Everyone</option>
            <option value="active">Active</option>
            <option value="disabled">Suspended</option>
            <option value="admins">Admins</option>
          </Select>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="users-sort">Sort</Label>
          <Select id="users-sort" value={sort.value} onChange={(e) => setSearch({ sort: e.target.value === "newest" ? null : e.target.value, page: null })}>
            {SORTS.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </Select>
        </div>
      </div>

      {list.error && !list.data ? <ErrorBox error={list.error} onRetry={list.reload} /> : null}
      {!list.data && list.loading ? <Spinner /> : null}
      {list.data ? (
        <div aria-busy={list.loading}>
          {list.data.users.length === 0 ? (
            <EmptyState>{q || status !== "all" ? "No users match." : "No users yet."}</EmptyState>
          ) : (
            <>
              {/* Table on wider screens */}
              <div className="hidden overflow-x-auto rounded-lg border-2 border-primary bg-card shadow-brutal md:block">
                <table className="w-full text-left text-sm">
                  <caption className="sr-only">Users</caption>
                  <thead className="border-b-2 border-primary bg-kuning-100 font-azeret text-xs uppercase tracking-wide text-primary-400">
                    <tr>
                      <th scope="col" className="px-4 py-3">User</th>
                      <th scope="col" className="px-4 py-3">Contact</th>
                      <th scope="col" className="px-4 py-3">Joined</th>
                      <th scope="col" className="px-4 py-3">Last seen</th>
                      <th scope="col" className="px-4 py-3 text-right">Moments</th>
                      <th scope="col" className="px-4 py-3 text-right">Storage</th>
                      <th scope="col" className="px-4 py-3 text-right">Friends</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {list.data.users.map((u) => (
                      <tr key={u.id} className={cn("hover:bg-soft", u.disabled_at && "opacity-70")}>
                        <td className="px-4 py-3">
                          <div className="flex items-center gap-3">
                            <Avatar user={u} size="sm" />
                            <div className="min-w-0">
                              <Link
                                to={toUser(u.id)}
                                className="block truncate font-bold underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                {u.display_name}
                              </Link>
                              <span className="block truncate font-azeret text-xs text-muted-foreground">
                                {u.username ? "@" + u.username : "no username"}
                              </span>
                            </div>
                            <UserBadges user={u} />
                          </div>
                        </td>
                        <td className="max-w-[14rem] px-4 py-3">{contact(u)}</td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <Time ms={u.created_at} />
                        </td>
                        <td className="whitespace-nowrap px-4 py-3">
                          <Time ms={u.last_seen_at} />
                        </td>
                        <td className="px-4 py-3 text-right font-azeret tabular">{num(u.moments)}</td>
                        <td className="whitespace-nowrap px-4 py-3 text-right font-azeret tabular">{bytes(u.storage_bytes)}</td>
                        <td className="px-4 py-3 text-right font-azeret tabular">{num(u.friends)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Cards on phones */}
              <ul className="grid gap-3 md:hidden">
                {list.data.users.map((u) => (
                  <li key={u.id}>
                    <Link
                      to={toUser(u.id)}
                      className={cn(
                        "block rounded-lg border-2 border-primary bg-card p-4 shadow-brutal-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                        u.disabled_at && "opacity-75",
                      )}
                    >
                      <div className="flex items-center gap-3">
                        <Avatar user={u} />
                        <div className="min-w-0 flex-1">
                          <span className="block truncate font-bold">{u.display_name}</span>
                          <span className="block truncate font-azeret text-xs text-muted-foreground">
                            {u.username ? "@" + u.username : "no username"}
                          </span>
                        </div>
                        <UserBadges user={u} className="justify-end" />
                      </div>
                      <div className="mt-3 text-xs">{contact(u)}</div>
                      <dl className="mt-3 grid grid-cols-3 gap-2 border-t border-border pt-3 text-center">
                        <div>
                          <dt className="font-azeret text-[10px] uppercase text-muted-foreground">Moments</dt>
                          <dd className="font-azeret text-sm font-bold tabular">{num(u.moments)}</dd>
                        </div>
                        <div>
                          <dt className="font-azeret text-[10px] uppercase text-muted-foreground">Storage</dt>
                          <dd className="font-azeret text-sm font-bold tabular">{bytes(u.storage_bytes)}</dd>
                        </div>
                        <div>
                          <dt className="font-azeret text-[10px] uppercase text-muted-foreground">Friends</dt>
                          <dd className="font-azeret text-sm font-bold tabular">{num(u.friends)}</dd>
                        </div>
                      </dl>
                      <p className="mt-2 text-xs text-muted-foreground">
                        Joined <Time ms={u.created_at} /> · seen <Time ms={u.last_seen_at} />
                      </p>
                    </Link>
                  </li>
                ))}
              </ul>
            </>
          )}
          <Pagination page={list.data.page} perPage={list.data.per_page} total={list.data.total} onPage={(p) => setSearch({ page: p === 1 ? null : String(p) }, false)} />
        </div>
      ) : null}

      <NewUserDialog open={newOpen} onOpenChange={setNewOpen} onCreated={list.reload} />
      <UserDrawer
        userID={userID}
        onClose={() => navigate("/users" + search)}
        onChanged={list.reload}
      />
    </>
  );
}

function NewUserDialog({ open, onOpenChange, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: () => void }) {
  const toast = useToast();
  const [identity, setIdentity] = React.useState<Identity>(emptyIdentity);
  const [admin, setAdmin] = React.useState(false);
  const [touched, setTouched] = React.useState(false);
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const [result, setResult] = React.useState<{ user: AdminUser; password: string } | null>(null);

  React.useEffect(() => {
    if (open) {
      setIdentity(emptyIdentity);
      setAdmin(false);
      setTouched(false);
      setError(null);
      setResult(null);
    }
  }, [open]);

  const errors = identityErrors(identity, false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setTouched(true);
    if (Object.keys(errors).length) return;
    setPending(true);
    setError(null);
    try {
      const res = await post<{ user: AdminUser; password: string }>("users", {
        display_name: identity.display_name.trim(),
        username: identity.username || null,
        method: identity.method,
        identifier: identity.identifier.trim(),
        admin,
      });
      setResult(res);
      onCreated();
      toast(`Created ${res.user.display_name}.`);
    } catch (err) {
      setError(err);
    } finally {
      setPending(false);
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <AlertDialogContent className="max-w-xl">
        {result ? (
          <>
            <AlertDialogHeader>
              <AlertDialogTitle>Account created</AlertDialogTitle>
              <AlertDialogDescription>
                Give {result.user.display_name} this one-time password over a channel you trust. They sign in with their{" "}
                {identity.method === "email" ? "email address" : "phone number"} and should change it in the app.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <CopyField id="new-user-password" label="One-time password" value={result.password} />
            <Alert variant="warning" role="status">
              <AlertDescription>This password is shown only once.</AlertDescription>
            </Alert>
            <AlertDialogFooter>
              <AlertDialogCancel>Done</AlertDialogCancel>
            </AlertDialogFooter>
          </>
        ) : (
          <form className="grid gap-4" onSubmit={submit} noValidate>
            <AlertDialogHeader>
              <AlertDialogTitle>New user</AlertDialogTitle>
              <AlertDialogDescription>
                Creates an account with a one-time password. Useful when registration is closed; otherwise an invite
                is usually easier.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <IdentityFields idPrefix="new-user" value={identity} onChange={setIdentity} errors={touched ? errors : {}} usernameRequired={false} />
            <div className="flex items-center justify-between gap-4 rounded-md border-2 border-primary p-3">
              <Label htmlFor="new-user-admin" className="normal-case tracking-normal">
                <span className="block text-sm font-bold">Admin</span>
                <span className="mt-1 block font-sans text-xs font-normal text-muted-foreground">Can open this panel.</span>
              </Label>
              <Switch id="new-user-admin" checked={admin} onCheckedChange={setAdmin} />
            </div>
            <FormError error={error} />
            <AlertDialogFooter>
              <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
              <Button type="submit" disabled={pending}>
                {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
                Create user
              </Button>
            </AlertDialogFooter>
          </form>
        )}
      </AlertDialogContent>
    </AlertDialog>
  );
}
