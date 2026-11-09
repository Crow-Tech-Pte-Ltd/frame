# Frame admin panel (`admin-ui/`)

The web dashboard served by `frame-server` at **`/admin/`** — users, invites,
settings, two-factor, audit log, backups. Features, security model and the JSON
API it talks to are documented in [`docs/ADMIN.md`](../docs/ADMIN.md).

Vite 8 + React 19 + TypeScript + Tailwind 3, styled with the Crow Tech website's
design system (components, tokens and fonts copied — not imported — from the
website's source).

## How it ships

`bun run build` writes the static site to
**`server/internal/adminui/dist/`**, which is committed. The Go package
`server/internal/adminui` embeds it with `go:embed`, so **`go build` never needs
Node or Bun** — only people changing the panel do.

The server serves `/admin/assets/*` (content-hashed file names) with a one-year
immutable cache, any other existing file under `dist/` (e.g. `licenses/`)
as-is, and `index.html` (no-store) for every other `/admin/…` path, so
client-side routes like `/admin/users/<id>` work on reload.

## Develop

```sh
# 1. a dev server with seeded data (alice / password123 is an admin)
cd server && go run ./cmd/frame-server dev            # http://127.0.0.1:8787

# 2. the panel with hot reload, proxying /admin/api to that server
cd admin-ui
bun install --frozen-lockfile
bun run dev                                           # http://localhost:5173/admin/
```

Point the proxy elsewhere with `FRAME_DEV_SERVER=http://127.0.0.1:8788 bun run dev`.
The proxy rewrites the `Origin` header to the target's origin, because the
server's CSRF check requires same-origin requests.

Other scripts: `bun run typecheck`, `bun run build` (type-check → Vite build →
`scripts/check-dist.mjs`, which fails on anything that would break the CSP),
`bun run preview`.

## Constraints

- **Strict CSP**: the server sends `default-src 'self'; img-src 'self' data: blob:;
  style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none';
  form-action 'self'`. So: no inline `<script>`/`<style>`, no `style=""` in
  markup, no third-party URLs, no `eval`. `build.modulePreload.polyfill` is off
  (it would be an inline script) and `assetsInlineLimit` is 0 (fonts and images
  are files, never `data:` URLs).
- **Dialogs use the native `<dialog>`** (`showModal()`) instead of Radix
  Dialog/AlertDialog: Radix's scroll lock (`react-remove-scroll`) injects a
  `<style>` element at runtime, which `style-src 'self'` blocks. The native
  element gives the top layer, an inert background, focus containment and Esc
  for free; `components/ui/alert-dialog.tsx` keeps the website's names and look.
- **Theme**: light/dark follows `prefers-color-scheme` purely in CSS (no inline
  boot script needed); the System/Light/Dark switch adds `.light`/`.dark` to
  `<html>` and remembers the choice in `localStorage`. The website's offset
  "neo-brutalist" shadows use a `--shadow` token (black in light mode, yellow in
  dark mode) so they stay visible.
- **Privacy**: the panel never shows anyone's photos, captions or messages —
  the API doesn't return them. Avatars load from `/admin/api/v1/users/{id}/avatar`.
  The one exception is a member's report: its drawer shows exactly the
  reported item, with media from `/admin/api/v1/reports/{id}/media` (see
  `docs/ADMIN.md`, "Reports and the content exception").
- **No external requests at runtime**: fonts, logo and favicon are bundled.
- Minimal dependencies, all pinned exactly (`package.json` + `bun.lock`):
  React, Radix Slot/Label/Switch, class-variance-authority, clsx,
  tailwind-merge 2 (for Tailwind 3), lucide-react, qrcode-generator (QR codes
  are drawn as a single SVG path from its module matrix). No router library
  (`src/lib/router.tsx` is ~100 lines over the History API).

## Layout

```
src/
  main.tsx, App.tsx        boot, session check, routes
  app-context.tsx          session + toast contexts
  index.css                @font-face, Crow tokens (light/dark), base styles
  lib/                     api client, router, theme, formatting, validation, types
  components/ui/           copied Crow Tech components (button, card, badge, input,
                           label, switch, alert, textarea) + native alert-dialog
  components/              layout, shared bits, QR code, dialogs
  pages/                   Setup, Login, Overview, Users (+ UserDrawer), Reports, Invites,
                           Settings, Security, Audit, Maintenance
public/licenses/           font licences (copied to dist/licenses/)
```

## Fonts and licences

- **Ubuntu** 400/700 (latin + latin-ext) — Ubuntu Font Licence 1.0
  (`public/licenses/UbuntuFontLicence-1.0.txt`).
- **Azeret Mono** variable (latin + latin-ext) — SIL Open Font License 1.1
  (`public/licenses/AzeretMono-OFL.txt`).

Both are the same woff2 subsets the Crow Tech website builds via `next/font`,
self-hosted here. The Crow Tech logo and favicon are Crow Tech assets.
