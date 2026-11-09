// Session + toast contexts shared by every page.
import * as React from "react";
import { CheckCircle2, XCircle } from "lucide-react";

import type { SessionInfo } from "@/lib/types";
import { cn } from "@/lib/utils";

type SessionCtx = {
  session: SessionInfo | null;
  setSession: (s: SessionInfo | null) => void;
  refresh: () => Promise<void>;
};

export const SessionContext = React.createContext<SessionCtx | null>(null);

export function useSession(): SessionCtx {
  const ctx = React.useContext(SessionContext);
  if (!ctx) throw new Error("useSession outside provider");
  return ctx;
}

/** The signed-in admin (only call inside the authenticated layout). */
export function useAdmin(): SessionInfo {
  const { session } = useSession();
  if (!session) throw new Error("useAdmin without a session");
  return session;
}

type Toast = { id: number; message: string; kind: "success" | "error" };
type ToastCtx = (message: string, kind?: Toast["kind"]) => void;
const ToastContext = React.createContext<ToastCtx>(() => {});

export function useToast(): ToastCtx {
  return React.useContext(ToastContext);
}

export function ToastProvider({ children }: { children: React.ReactNode }) {
  const [toasts, setToasts] = React.useState<Toast[]>([]);
  const push = React.useCallback<ToastCtx>((message, kind = "success") => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t.slice(-2), { id, message, kind }]);
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 5000);
  }, []);
  return (
    <ToastContext.Provider value={push}>
      {children}
      <div
        aria-live="polite"
        aria-atomic="false"
        className="pointer-events-none fixed inset-x-4 bottom-4 z-[60] flex flex-col items-end gap-2 sm:left-auto sm:right-6 sm:w-96"
      >
        {toasts.map((t) => (
          <div
            key={t.id}
            role={t.kind === "error" ? "alert" : "status"}
            className={cn(
              "pointer-events-auto flex w-full items-start gap-2 rounded-lg border-2 border-primary-400 px-4 py-3 text-sm font-medium text-primary-400 shadow-brutal-sm",
              t.kind === "success" ? "bg-success-50" : "bg-danger-50",
            )}
          >
            {t.kind === "success" ? (
              <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
            ) : (
              <XCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
            )}
            <span>{t.message}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
