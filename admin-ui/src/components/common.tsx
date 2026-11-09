// Small shared building blocks for the panel pages.
import * as React from "react";
import { AlertCircle, Check, Copy, Loader2 } from "lucide-react";

import { avatarURL, errorMessage } from "@/lib/api";
import { absolute, initials, relative } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Button, type ButtonProps } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";

export function Time({ ms, mode = "relative", className }: { ms: number | null; mode?: "relative" | "absolute"; className?: string }) {
  if (ms === null || ms === undefined) return <span className={cn("text-muted-foreground", className)}>—</span>;
  const iso = new Date(ms).toISOString();
  return (
    <time dateTime={iso} title={absolute(ms)} className={className}>
      {mode === "relative" ? relative(ms) : absolute(ms)}
    </time>
  );
}

export function Avatar({
  user,
  size = "md",
}: {
  user: { id: string; display_name: string; has_avatar?: boolean };
  size?: "sm" | "md" | "lg";
}) {
  const [failed, setFailed] = React.useState(false);
  const cls = size === "sm" ? "h-8 w-8 text-[11px]" : size === "lg" ? "h-14 w-14 text-base" : "h-10 w-10 text-xs";
  if (user.has_avatar && !failed) {
    return (
      <img
        src={avatarURL(user.id)}
        alt=""
        width={56}
        height={56}
        loading="lazy"
        onError={() => setFailed(true)}
        className={cn(cls, "shrink-0 rounded-full border-2 border-primary object-cover bg-muted")}
      />
    );
  }
  return (
    <span
      aria-hidden="true"
      className={cn(
        cls,
        "inline-flex shrink-0 items-center justify-center rounded-full border-2 border-primary bg-kuning-100 font-azeret font-bold text-primary-400",
      )}
    >
      {initials(user.display_name)}
    </span>
  );
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  return (
    <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <h1 className="font-azeret text-2xl font-bold tracking-tight sm:text-3xl">{title}</h1>
        {description ? <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{description}</p> : null}
      </div>
      {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
    </div>
  );
}

export function Spinner({ label = "Loading…", className }: { label?: string; className?: string }) {
  return (
    <div role="status" className={cn("flex items-center gap-2 py-8 text-sm text-muted-foreground", className)}>
      <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
      <span>{label}</span>
    </div>
  );
}

export function ErrorBox({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  return (
    <Alert variant="destructive">
      <AlertCircle className="h-4 w-4" />
      <AlertDescription className="flex flex-wrap items-center gap-3">
        <span>{errorMessage(error)}</span>
        {onRetry ? (
          <Button size="sm" variant="outline" onClick={onRetry}>
            Try again
          </Button>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

export function FormError({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p role="alert" className="flex items-start gap-2 text-sm font-medium text-destructive">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      <span>{errorMessage(error)}</span>
    </p>
  );
}

export function Field({
  id,
  label,
  hint,
  children,
  className,
}: {
  id: string;
  label: React.ReactNode;
  hint?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("grid gap-2", className)}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint ? (
        <p id={id + "-hint"} className="text-xs text-muted-foreground">
          {hint}
        </p>
      ) : null}
    </div>
  );
}

export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, ...props }, ref) => (
    <select
      ref={ref}
      className={cn(
        "h-10 w-full rounded-md border border-input bg-background px-3 text-base shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60 sm:text-sm",
        className,
      )}
      {...props}
    />
  ),
);
Select.displayName = "Select";

async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through to the legacy path
  }
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.className = "fixed -left-[9999px] top-0 opacity-0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  }
}

export function CopyButton({ text, label = "Copy", ...props }: { text: string; label?: string } & ButtonProps) {
  const [state, setState] = React.useState<"idle" | "ok" | "fail">("idle");
  React.useEffect(() => {
    if (state === "idle") return;
    const t = window.setTimeout(() => setState("idle"), 2000);
    return () => window.clearTimeout(t);
  }, [state]);
  return (
    <Button
      variant="outline"
      size="sm"
      onClick={async () => setState((await copyText(text)) ? "ok" : "fail")}
      {...props}
    >
      {state === "ok" ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
      <span aria-live="polite">{state === "ok" ? "Copied" : state === "fail" ? "Select & copy" : label}</span>
    </Button>
  );
}

/** A read-only value with a copy button (one-time passwords, links). */
export function CopyField({ id, value, label, mono = true }: { id: string; value: string; label: string; mono?: boolean }) {
  return (
    <div className="grid min-w-0 gap-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex min-w-0 gap-2">
        <input
          id={id}
          readOnly
          value={value}
          onFocus={(e) => e.currentTarget.select()}
          className={cn(
            "h-10 min-w-0 flex-1 rounded-md border-2 border-primary bg-soft px-3 text-sm text-soft-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            mono && "font-azeret",
          )}
        />
        <CopyButton text={value} className="h-10" />
      </div>
    </div>
  );
}

/** Stat tile: label + big number, website "Stat" pattern. */
export function Stat({ label, value, hint }: { label: string; value: React.ReactNode; hint?: React.ReactNode }) {
  return (
    <div className="rounded-lg border-2 border-primary bg-card p-4 shadow-brutal-sm">
      <div className="font-azeret text-[11px] font-bold uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className="mt-2 font-azeret text-2xl font-bold tabular tracking-normal">{value}</div>
      {hint ? <div className="mt-1 text-xs text-muted-foreground">{hint}</div> : null}
    </div>
  );
}

export function EmptyState({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-lg border-2 border-dashed border-border px-4 py-10 text-center text-sm text-muted-foreground">
      {children}
    </div>
  );
}

export function Pagination({
  page,
  perPage,
  total,
  onPage,
}: {
  page: number;
  perPage: number;
  total: number;
  onPage: (p: number) => void;
}) {
  const pages = Math.max(1, Math.ceil(total / perPage));
  const from = total === 0 ? 0 : (page - 1) * perPage + 1;
  const to = Math.min(total, page * perPage);
  return (
    <nav aria-label="Pagination" className="mt-4 flex flex-wrap items-center justify-between gap-3 text-sm">
      <p className="text-muted-foreground" aria-live="polite">
        {total === 0 ? "No results" : `${from}–${to} of ${total}`}
      </p>
      <div className="flex items-center gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => onPage(page - 1)}>
          Previous
        </Button>
        <span className="font-azeret text-xs tabular">
          {page} / {pages}
        </span>
        <Button variant="outline" size="sm" disabled={page >= pages} onClick={() => onPage(page + 1)}>
          Next
        </Button>
      </div>
    </nav>
  );
}

export const PRIVACY_NOTE = "Frame's admin panel doesn't show anyone's photos or messages, except what members report to you.";

/** Loads data with reload support; aborts on unmount or key change. */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>, deps: React.DependencyList) {
  const [state, setState] = React.useState<{ data: T | null; error: unknown; loading: boolean }>({
    data: null,
    error: null,
    loading: true,
  });
  const [tick, setTick] = React.useState(0);
  React.useEffect(() => {
    const ctrl = new AbortController();
    setState((s) => ({ ...s, loading: true, error: null }));
    load(ctrl.signal).then(
      (data) => {
        if (!ctrl.signal.aborted) setState({ data, error: null, loading: false });
      },
      (error) => {
        if (!ctrl.signal.aborted) setState((s) => ({ data: s.data, error, loading: false }));
      },
    );
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, tick]);
  const reload = React.useCallback(() => setTick((t) => t + 1), []);
  return { ...state, reload, setData: (d: T) => setState({ data: d, error: null, loading: false }) };
}
