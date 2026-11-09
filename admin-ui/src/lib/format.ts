// Formatting helpers: dates, durations, bytes, numbers.

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
const nf = new Intl.NumberFormat();
const dtf = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const df = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

export function absolute(ms: number): string {
  return dtf.format(new Date(ms));
}

export function dateOnly(ms: number): string {
  return df.format(new Date(ms));
}

/** "3 minutes ago", "in 6 days", "yesterday". */
export function relative(ms: number, now = Date.now()): string {
  const diff = ms - now;
  const abs = Math.abs(diff);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 365 * 86400e3],
    ["month", 30 * 86400e3],
    ["week", 7 * 86400e3],
    ["day", 86400e3],
    ["hour", 3600e3],
    ["minute", 60e3],
  ];
  for (const [unit, size] of units) {
    if (abs >= size) return rtf.format(Math.round(diff / size), unit);
  }
  return abs < 45e3 ? "just now" : rtf.format(Math.round(diff / 60e3), "minute");
}

export function duration(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export function bytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export function num(n: number): string {
  return nf.format(n);
}

export function plural(n: number, one: string, many = one + "s"): string {
  return `${num(n)} ${n === 1 ? one : many}`;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  const first = Array.from(parts[0])[0] ?? "";
  const second = parts.length > 1 ? (Array.from(parts[parts.length - 1])[0] ?? "") : "";
  return (first + second).toUpperCase();
}

export function userLabel(u: { username: string | null; display_name: string }): string {
  return u.username ? `@${u.username}` : u.display_name;
}
