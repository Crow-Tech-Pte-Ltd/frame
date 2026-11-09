// Client-side checks mirroring the server's rules (docs/API.md, Me).

export function usernameError(u: string): string | null {
  if (u.length < 3) return "Must be at least 3 characters";
  if (u.length > 20) return "Can't be longer than 20 characters";
  if (!/^[a-z0-9._]+$/.test(u)) return "Only letters, numbers, _ and . allowed";
  if (!/^[a-z]/.test(u)) return "Must start with a letter";
  if (!/[a-z0-9]$/.test(u)) return "Must end with a letter or number";
  if (/[._]{2}/.test(u)) return "Can't contain consecutive . or _";
  return null;
}

export function passwordError(p: string): string | null {
  if (p.length < 8) return "Use at least 8 characters";
  if (p.length > 128) return "Use at most 128 characters";
  return null;
}

export function identifierError(method: "email" | "phone", v: string): string | null {
  const s = v.trim();
  if (method === "email") return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s) ? null : "Enter a valid email address";
  return /^\+[1-9]\d{6,14}$/.test(s) ? null : "Use international format, e.g. +34600111222";
}
