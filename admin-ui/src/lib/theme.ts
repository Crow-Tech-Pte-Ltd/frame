// Light/dark follows the system by default (pure CSS, see index.css), with a
// manual override stored per browser. No inline script is needed: the CSS
// media query already paints the right theme before this module runs.
import * as React from "react";

export type ThemeChoice = "system" | "light" | "dark";
const KEY = "frame-admin-theme";

export function loadTheme(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    // storage blocked: fall back to the system preference
  }
  return "system";
}

export function applyTheme(choice: ThemeChoice) {
  const root = document.documentElement;
  root.classList.toggle("dark", choice === "dark");
  root.classList.toggle("light", choice === "light");
}

let current: ThemeChoice = "system";
const listeners = new Set<() => void>();

export function initTheme() {
  current = loadTheme();
  applyTheme(current);
}

export function setTheme(choice: ThemeChoice) {
  current = choice;
  applyTheme(choice);
  try {
    if (choice === "system") window.localStorage.removeItem(KEY);
    else window.localStorage.setItem(KEY, choice);
  } catch {
    // ignore
  }
  listeners.forEach((l) => l());
}

export function useTheme(): ThemeChoice {
  return React.useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}
