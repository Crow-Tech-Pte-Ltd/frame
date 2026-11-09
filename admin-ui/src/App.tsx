import * as React from "react";

import { SessionContext, ToastProvider } from "@/app-context";
import { get, onAuthFailure } from "@/lib/api";
import { match, navigate, useLocation } from "@/lib/router";
import type { SessionInfo } from "@/lib/types";
import { Layout } from "@/components/layout";
import { Spinner } from "@/components/common";
import { LoginPage } from "@/pages/Login";
import { SetupPage } from "@/pages/Setup";
import { OverviewPage } from "@/pages/Overview";
import { UsersPage } from "@/pages/Users";
import { ReportsPage } from "@/pages/Reports";
import { InvitesPage } from "@/pages/Invites";
import { SettingsPage } from "@/pages/Settings";
import { SecurityPage } from "@/pages/Security";
import { AuditPage } from "@/pages/Audit";
import { MaintenancePage } from "@/pages/Maintenance";
import { NotFoundPage } from "@/pages/NotFound";

const PUBLIC = new Set(["/login", "/setup"]);

const TITLES: Record<string, string> = {
  "/": "Overview",
  "/users": "Users",
  "/reports": "Reports",
  "/invites": "Invites",
  "/settings": "Settings",
  "/security": "Security",
  "/audit": "Audit log",
  "/maintenance": "Maintenance",
  "/login": "Sign in",
  "/setup": "Set up",
};

function currentPathWithSearch() {
  const p = window.location.pathname.replace(/^\/admin/, "") || "/";
  return p + window.location.search;
}

export function App() {
  const { path } = useLocation();
  const [session, setSession] = React.useState<SessionInfo | null>(null);
  const [checked, setChecked] = React.useState(false);

  const refresh = React.useCallback(async () => {
    try {
      setSession(await get<SessionInfo>("session", { session: false }));
    } catch {
      setSession(null);
    } finally {
      setChecked(true);
    }
  }, []);

  React.useEffect(() => {
    void refresh();
  }, [refresh]);

  // A session-required call came back 401/403: drop the session and send
  // the admin to the sign-in page, remembering where they were.
  React.useEffect(() => {
    onAuthFailure((kind) => {
      setSession(null);
      const here = currentPathWithSearch();
      const next = PUBLIC.has(here.split("?")[0]) ? "/" : here;
      navigate(`/login?reason=${kind === "forbidden" ? "forbidden" : "expired"}&next=${encodeURIComponent(next)}`, {
        replace: true,
      });
    });
    return () => onAuthFailure(null);
  }, []);

  React.useEffect(() => {
    const base =
      TITLES[path] ?? (path.startsWith("/users/") ? "User" : path.startsWith("/reports/") ? "Report" : "Frame admin");
    document.title = session ? `${base} · ${session.server.name} · Frame admin` : `${base} · Frame admin`;
  }, [path, session]);

  const isPublic = PUBLIC.has(path);

  // Unauthenticated visits to private pages: go to setup (fresh server) or
  // sign-in.
  React.useEffect(() => {
    if (!checked || session || isPublic) return;
    let cancelled = false;
    get<{ needed: boolean }>("setup", { session: false })
      .then((s) => {
        if (cancelled) return;
        if (s.needed) navigate("/setup", { replace: true });
        else navigate(`/login?next=${encodeURIComponent(currentPathWithSearch())}`, { replace: true });
      })
      .catch(() => {
        if (!cancelled) navigate(`/login?next=${encodeURIComponent(currentPathWithSearch())}`, { replace: true });
      });
    return () => {
      cancelled = true;
    };
  }, [checked, session, isPublic]);

  const ctx = React.useMemo(() => ({ session, setSession, refresh }), [session, refresh]);

  let content: React.ReactNode;
  if (path === "/setup") content = <SetupPage />;
  else if (path === "/login") content = <LoginPage />;
  else if (!checked || !session) content = <Spinner className="justify-center py-24" />;
  else content = <Layout>{route(path)}</Layout>;

  return (
    <SessionContext.Provider value={ctx}>
      <ToastProvider>{content}</ToastProvider>
    </SessionContext.Provider>
  );
}

function route(path: string): React.ReactNode {
  if (path === "/") return <OverviewPage />;
  if (path === "/users") return <UsersPage userID={null} />;
  const u = match("/users/:id", path);
  if (u) return <UsersPage userID={u.id} />;
  if (path === "/reports") return <ReportsPage reportID={null} />;
  const rep = match("/reports/:id", path);
  if (rep) return <ReportsPage reportID={rep.id} />;
  if (path === "/invites") return <InvitesPage />;
  if (path === "/settings") return <SettingsPage />;
  if (path === "/security") return <SecurityPage />;
  if (path === "/audit") return <AuditPage />;
  if (path === "/maintenance") return <MaintenancePage />;
  return <NotFoundPage />;
}
