import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";

// The Frame server embeds the build output (server/internal/adminui, go:embed)
// and serves it at /admin/ under a strict CSP (no inline scripts or styles).
const devServer = process.env.FRAME_DEV_SERVER ?? "http://127.0.0.1:8787";
const devOrigin = new URL(devServer).origin;

export default defineConfig({
  base: "/admin/",
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  oxc: {
    jsx: { runtime: "automatic" },
  },
  build: {
    outDir: "../server/internal/adminui/dist",
    emptyOutDir: true,
    assetsDir: "assets",
    // Never inline assets as data: URLs (fonts must come from 'self').
    assetsInlineLimit: 0,
    // The polyfill would be injected as an inline script; every target
    // browser supports modulepreload natively.
    modulePreload: { polyfill: false },
    sourcemap: false,
    target: "es2022",
    chunkSizeWarningLimit: 600,
    rolldownOptions: {
      onwarn(warning, warn) {
        // Radix/lucide ship "use client" directives; irrelevant for an SPA.
        if (warning.code === "MODULE_LEVEL_DIRECTIVE") return;
        warn(warning);
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/admin/api": {
        target: devServer,
        changeOrigin: true,
        // The server's CSRF check compares Origin with its own address.
        configure: (proxy) => {
          proxy.on("proxyReq", (req) => {
            if (req.getHeader("origin")) req.setHeader("origin", devOrigin);
          });
        },
      },
    },
  },
});
