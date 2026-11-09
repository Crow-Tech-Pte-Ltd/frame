// Tiny client for the admin API (/admin/api/v1, docs/ADMIN.md §7).
// Every state-changing request carries the CSRF header X-Frame-Admin: 1;
// the browser adds Origin / Sec-Fetch-Site and the SameSite=Strict cookie.

export const API_BASE = "/admin/api/v1";

export class ApiError extends Error {
  status: number;
  code: string;
  retryAfter: number | null;
  constructor(status: number, code: string, message: string, retryAfter: number | null = null) {
    super(message);
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

type AuthListener = (kind: "unauthorized" | "forbidden") => void;
let authListener: AuthListener | null = null;

/** Called when a session-required request finds no session (401) or a
 *  session whose account is no longer an admin (403 forbidden). */
export function onAuthFailure(fn: AuthListener | null) {
  authListener = fn;
}

export type RequestOptions = {
  /** false for public endpoints (login, setup): never trigger the global
   *  sign-in redirect. */
  session?: boolean;
  signal?: AbortSignal;
};

export async function api<T>(
  method: "GET" | "POST" | "PATCH" | "DELETE",
  path: string,
  body?: unknown,
  opts: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") headers["X-Frame-Admin"] = "1";
  let payload: BodyInit | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(API_BASE + "/" + path.replace(/^\//, ""), {
      method,
      headers,
      body: payload,
      credentials: "same-origin",
      cache: "no-store",
      signal: opts.signal,
    });
  } catch (err) {
    if ((err as Error).name === "AbortError") throw err;
    throw new ApiError(0, "network", "Can't reach the server. Check your connection and try again.");
  }
  if (res.status === 204) return undefined as T;
  let data: unknown = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const e = (data as { error?: { code?: string; message?: string } } | null)?.error;
    const code = e?.code ?? (res.status === 404 ? "not_found" : "http_" + res.status);
    const ra = res.headers.get("Retry-After");
    const err = new ApiError(res.status, code, e?.message ?? res.statusText ?? "Request failed", ra ? Number(ra) : null);
    if (opts.session !== false && authListener) {
      if (res.status === 401 && code === "unauthorized") authListener("unauthorized");
      else if (res.status === 403 && code === "forbidden") authListener("forbidden");
    }
    throw err;
  }
  return data as T;
}

export const get = <T>(path: string, opts?: RequestOptions) => api<T>("GET", path, undefined, opts);
export const post = <T>(path: string, body?: unknown, opts?: RequestOptions) => api<T>("POST", path, body ?? {}, opts);
export const patch = <T>(path: string, body: unknown, opts?: RequestOptions) => api<T>("PATCH", path, body, opts);
export const del = <T>(path: string, body?: unknown, opts?: RequestOptions) => api<T>("DELETE", path, body, opts);

/** Human message for an error, with a few codes rephrased for people. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "rate_limited":
        return `Too many attempts. Try again in ${err.retryAfter ?? 60} seconds.`;
      case "invalid_credentials":
        return "That didn't match. Check the details and try again.";
      case "totp_invalid":
        return "That code didn't work. Codes change every 30 seconds — try the current one.";
      case "csrf_failed":
        return "The request was blocked as cross-site. Reload the page and try again.";
      case "network":
        return err.message;
      default:
        return err.message || "Something went wrong.";
    }
  }
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}

export const avatarURL = (userID: string) => `${API_BASE}/users/${encodeURIComponent(userID)}/avatar`;
