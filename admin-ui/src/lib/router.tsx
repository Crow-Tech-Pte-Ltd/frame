// Minimal history-API router for the panel (everything lives under /admin).
import * as React from "react";

export const BASE = "/admin";

type Loc = { path: string; search: string };

function read(): Loc {
  let p = window.location.pathname;
  if (p.startsWith(BASE)) p = p.slice(BASE.length);
  if (!p.startsWith("/")) p = "/" + p;
  if (p.length > 1 && p.endsWith("/")) p = p.slice(0, -1);
  return { path: p, search: window.location.search };
}

let current = read();
const listeners = new Set<() => void>();
function emit() {
  current = read();
  listeners.forEach((l) => l());
}
window.addEventListener("popstate", emit);

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useLocation(): Loc {
  return React.useSyncExternalStore(subscribe, () => current);
}

/** Navigate to an in-panel path such as "/users?status=disabled". */
export function navigate(to: string, opts: { replace?: boolean } = {}) {
  const url = BASE + (to === "/" ? "/" : to);
  if (opts.replace) window.history.replaceState(null, "", url);
  else window.history.pushState(null, "", url);
  emit();
  if (!opts.replace) window.scrollTo(0, 0);
}

export function useSearchParams(): URLSearchParams {
  const { search } = useLocation();
  return React.useMemo(() => new URLSearchParams(search), [search]);
}

/** Replace query parameters on the current path (null removes a key). */
export function setSearch(update: Record<string, string | null>, replace = true) {
  const loc = read();
  const params = new URLSearchParams(loc.search);
  for (const [k, v] of Object.entries(update)) {
    if (v === null || v === "") params.delete(k);
    else params.set(k, v);
  }
  const qs = params.toString();
  navigate(loc.path + (qs ? "?" + qs : ""), { replace });
}

type LinkProps = Omit<React.AnchorHTMLAttributes<HTMLAnchorElement>, "href"> & { to: string };

export const Link = React.forwardRef<HTMLAnchorElement, LinkProps>(({ to, onClick, ...props }, ref) => (
  <a
    ref={ref}
    href={BASE + (to === "/" ? "/" : to)}
    onClick={(e) => {
      onClick?.(e);
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      e.preventDefault();
      navigate(to);
    }}
    {...props}
  />
));
Link.displayName = "Link";

/** Matches "/users/:id" style patterns; returns params or null. */
export function match(pattern: string, path: string): Record<string, string> | null {
  const a = pattern.split("/").filter(Boolean);
  const b = path.split("/").filter(Boolean);
  if (a.length !== b.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < a.length; i++) {
    if (a[i].startsWith(":")) params[a[i].slice(1)] = decodeURIComponent(b[i]);
    else if (a[i] !== b[i]) return null;
  }
  return params;
}
