// Authenticated shell: sidebar on desktop, top bar + menu on phones.
import * as React from "react";
import {
  Flag,
  LayoutDashboard,
  LogOut,
  Menu,
  Monitor,
  Moon,
  ScrollText,
  Settings as SettingsIcon,
  ShieldAlert,
  ShieldCheck,
  Sun,
  Ticket,
  Users as UsersIcon,
  Wrench,
  X,
} from "lucide-react";

import logo from "@/assets/crow_tech_logo.png";
import logoNegative from "@/assets/crow_tech_logo_negative.png";
import { useAdmin, useSession } from "@/app-context";
import { get, post } from "@/lib/api";
import { Link, navigate, useLocation } from "@/lib/router";
import { setTheme, useTheme, type ThemeChoice } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { PRIVACY_NOTE } from "@/components/common";

const NAV = [
  { to: "/", label: "Overview", icon: LayoutDashboard },
  { to: "/users", label: "Users", icon: UsersIcon },
  { to: "/reports", label: "Reports", icon: Flag },
  { to: "/invites", label: "Invites", icon: Ticket },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
  { to: "/security", label: "Security", icon: ShieldCheck },
  { to: "/audit", label: "Audit log", icon: ScrollText },
  { to: "/maintenance", label: "Maintenance", icon: Wrench },
];

function isActive(to: string, path: string) {
  return to === "/" ? path === "/" : path === to || path.startsWith(to + "/");
}

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("block", className)}>
      <img src={logo} alt="Crow Tech" width={600} height={270} className="only-light h-full w-auto" />
      <img src={logoNegative} alt="Crow Tech" width={689} height={267} className="only-dark h-full w-auto" />
    </span>
  );
}

export function ThemeSwitch({ className }: { className?: string }) {
  const choice = useTheme();
  const options: { value: ThemeChoice; label: string; icon: typeof Sun }[] = [
    { value: "system", label: "System", icon: Monitor },
    { value: "light", label: "Light", icon: Sun },
    { value: "dark", label: "Dark", icon: Moon },
  ];
  return (
    <div role="group" aria-label="Colour theme" className={cn("inline-flex rounded-md border-2 border-primary p-0.5", className)}>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={choice === o.value}
          title={o.label}
          onClick={() => setTheme(o.value)}
          className={cn(
            "inline-flex h-8 items-center gap-1.5 rounded-sm px-2 font-azeret text-xs font-bold focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            choice === o.value ? "bg-primary text-primary-foreground" : "hover:bg-soft",
          )}
        >
          <o.icon className="h-3.5 w-3.5" aria-hidden="true" />
          <span>{o.label}</span>
        </button>
      ))}
    </div>
  );
}

/** Open-report count for the nav badge: refreshed on every page change and
 *  whenever the Reports page resolves something. */
function useOpenReports(): number {
  const { path } = useLocation();
  const [open, setOpen] = React.useState(0);
  const [tick, setTick] = React.useState(0);
  React.useEffect(() => {
    const bump = () => setTick((t) => t + 1);
    window.addEventListener("frame:reports-changed", bump);
    return () => window.removeEventListener("frame:reports-changed", bump);
  }, []);
  React.useEffect(() => {
    const ctrl = new AbortController();
    get<{ open: number }>("reports?status=open&per_page=1", { signal: ctrl.signal }).then(
      (r) => setOpen(r.open),
      () => {},
    );
    return () => ctrl.abort();
  }, [path, tick]);
  return open;
}

function NavList({ onNavigate, openReports }: { onNavigate?: () => void; openReports: number }) {
  const { path } = useLocation();
  return (
    <ul className="grid gap-1">
      {NAV.map((n) => {
        const active = isActive(n.to, path);
        return (
          <li key={n.to}>
            <Link
              to={n.to}
              onClick={onNavigate}
              aria-current={active ? "page" : undefined}
              className={cn(
                "flex items-center gap-3 rounded-md border-2 px-3 py-2 font-azeret text-sm font-bold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                active
                  ? "border-primary-400 bg-kuning-200 text-primary-400 shadow-brutal-xs"
                  : "border-transparent hover:border-primary hover:bg-soft",
              )}
            >
              <n.icon className="h-4 w-4 shrink-0" aria-hidden="true" />
              {n.label}
              {n.to === "/reports" && openReports > 0 ? (
                <span className="ml-auto rounded-full border-2 border-primary-400 bg-warning-100 px-2 text-xs tabular text-primary-400">
                  {openReports}
                  <span className="sr-only"> open</span>
                </span>
              ) : null}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

function useSignOut() {
  const { setSession } = useSession();
  return async () => {
    try {
      await post("logout", {}, { session: false });
    } catch {
      // the cookie may already be gone
    }
    setSession(null);
    navigate("/login?reason=signed_out", { replace: true });
  };
}

export function Layout({ children }: { children: React.ReactNode }) {
  const session = useAdmin();
  const signOut = useSignOut();
  const [menuOpen, setMenuOpen] = React.useState(false);
  const { path } = useLocation();
  const openReports = useOpenReports();
  React.useEffect(() => setMenuOpen(false), [path]);
  const me = session.admin;

  return (
    <div className="min-h-dvh bg-background lg:grid lg:grid-cols-[17rem_minmax(0,1fr)]">
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-4 focus:top-4 focus:z-[70] focus:rounded-md focus:bg-kuning-200 focus:px-4 focus:py-2 focus:font-azeret focus:font-bold focus:text-primary-400"
      >
        Skip to content
      </a>

      {/* Desktop sidebar */}
      {/* The column (background + border) spans the whole page; its content
          sticks to the viewport. */}
      <aside className="hidden border-r-2 border-primary bg-page lg:block">
        <div className="lg:sticky lg:top-0 lg:flex lg:h-dvh lg:flex-col">
        <div className="border-b-2 border-primary px-5 py-5">
          <Link to="/" className="block rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Logo className="h-11" />
          </Link>
          <p className="mt-3 font-azeret text-[11px] font-bold uppercase tracking-wider text-muted-foreground">Frame admin</p>
          <p className="truncate font-azeret text-sm font-bold" title={session.server.name}>
            {session.server.name}
          </p>
        </div>
        <nav aria-label="Main" className="flex-1 overflow-y-auto px-3 py-4">
          <NavList openReports={openReports} />
        </nav>
        <div className="grid gap-3 border-t-2 border-primary px-5 py-4">
          <ThemeSwitch />
          <div className="text-sm">
            <p className="text-xs text-muted-foreground">Signed in as</p>
            <p className="truncate font-bold">{me.display_name}</p>
          </div>
          <Button variant="outline" size="sm" onClick={signOut} className="justify-start">
            <LogOut aria-hidden="true" />
            Sign out
          </Button>
        </div>
        </div>
      </aside>

      {/* Phone / tablet top bar */}
      <header className="sticky top-0 z-40 border-b-2 border-primary bg-page lg:hidden">
        <div className="flex items-center justify-between gap-3 px-4 py-3">
          <Link to="/" className="flex min-w-0 items-center gap-3 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Logo className="h-8 shrink-0" />
            <span className="truncate font-azeret text-sm font-bold">{session.server.name}</span>
          </Link>
          <Button
            variant="outline"
            size="icon"
            aria-expanded={menuOpen}
            aria-controls="mobile-menu"
            aria-label={menuOpen ? "Close menu" : "Open menu"}
            onClick={() => setMenuOpen((o) => !o)}
          >
            {menuOpen ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
          </Button>
        </div>
        <div id="mobile-menu" hidden={!menuOpen} className="border-t-2 border-primary bg-page px-4 pb-4 pt-3">
          <nav aria-label="Main">
            <NavList onNavigate={() => setMenuOpen(false)} openReports={openReports} />
          </nav>
          <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
            <ThemeSwitch />
            <Button variant="outline" size="sm" onClick={signOut}>
              <LogOut aria-hidden="true" />
              Sign out
            </Button>
          </div>
        </div>
      </header>

      <div className="flex min-w-0 flex-col">
        <main id="main" tabIndex={-1} className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 focus:outline-none sm:px-6 lg:px-10 lg:py-10">
          {!me.totp_enabled ? (
            <Alert variant="warning" className="mb-6">
              <ShieldAlert className="h-4 w-4" />
              <AlertTitle>Two-factor authentication is off</AlertTitle>
              <AlertDescription>
                Anyone who learns your password can open this panel.{" "}
                <Link to="/security" className="font-bold underline underline-offset-2">
                  Turn on two-factor authentication
                </Link>
                .
              </AlertDescription>
            </Alert>
          ) : null}
          {children}
        </main>
        <footer className="border-t-2 border-primary bg-page px-4 py-4 text-xs text-muted-foreground sm:px-6 lg:px-10">
          <div className="mx-auto flex max-w-6xl flex-col gap-1 sm:flex-row sm:items-center sm:justify-between">
            <p>{PRIVACY_NOTE}</p>
            <p className="font-azeret">Frame {session.server.version} · Crow Tech</p>
          </div>
        </footer>
      </div>
    </div>
  );
}
