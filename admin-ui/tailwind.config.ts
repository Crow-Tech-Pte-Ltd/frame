import type { Config } from "tailwindcss";

// Theme extension copied from the Crow Tech website (crow-tech-landing
// tailwind.config.ts). Additions for the admin panel are marked "panel:".
const config: Config = {
  darkMode: ["class"],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      fontFamily: {
        azeret: ["var(--font-azeret-mono)", "monospace"],
        sans: ["var(--font-ubuntu)", "sans-serif"],
      },
      colors: {
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
        popover: {
          DEFAULT: "hsl(var(--popover))",
          foreground: "hsl(var(--popover-foreground))",
        },
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
          50: "hsl(var(--primary-50))",
          100: "hsl(var(--primary-100))",
          200: "hsl(var(--primary-200))",
          300: "hsl(var(--primary-300))",
          // Not in the website's config: always-black ink for text on the
          // fixed light fills (badges, warning banner) in both themes.
          400: "hsl(var(--primary-400))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        biru: {
          50: "hsl(var(--biru-50))",
          100: "hsl(var(--biru-100))",
          200: "hsl(var(--biru-200))",
          300: "hsl(var(--biru-300))",
          400: "hsl(var(--biru-400))",
        },
        oren: {
          50: "hsl(var(--oren-50))",
          100: "hsl(var(--oren-100))",
          200: "hsl(var(--oren-200))",
          300: "hsl(var(--oren-300))",
          400: "hsl(var(--oren-400))",
        },
        kuning: {
          50: "hsl(var(--kuning-50))",
          100: "hsl(var(--kuning-100))",
          200: "hsl(var(--kuning-200))",
          300: "hsl(var(--kuning-300))",
          400: "hsl(var(--kuning-400))",
        },
        warning: {
          50: "hsl(var(--warning-50))",
          100: "hsl(var(--warning-100))",
          200: "hsl(var(--warning-200))",
          300: "hsl(var(--warning-300))",
          400: "hsl(var(--warning-400))",
        },
        success: {
          50: "hsl(var(--success-50))",
          100: "hsl(var(--success-100))",
          200: "hsl(var(--success-200))",
          300: "hsl(var(--success-300))",
          400: "hsl(var(--success-400))",
        },
        danger: {
          50: "hsl(var(--danger-50))",
          100: "hsl(var(--danger-100))",
          200: "hsl(var(--danger-200))",
          300: "hsl(var(--danger-300))",
          400: "hsl(var(--danger-400))",
        },
        surface: {
          50: "hsl(var(--surface-50))",
          100: "hsl(var(--surface-100))",
          200: "hsl(var(--surface-200))",
        },
        // panel: theme-aware fills (the brand scales above are fixed colours).
        page: "hsl(var(--page))",
        soft: {
          DEFAULT: "hsl(var(--soft))",
          foreground: "hsl(var(--soft-foreground))",
        },
        shadow: "hsl(var(--shadow))",
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
      // panel: the website's neo-brutalist offset shadows, with a themeable
      // colour so they stay visible in dark mode.
      boxShadow: {
        brutal: "7px 7px 0px -1px hsl(var(--shadow))",
        "brutal-sm": "4px 4px 0px -1px hsl(var(--shadow))",
        "brutal-xs": "3px 3px 0px 0px hsl(var(--shadow))",
      },
    },
  },
  plugins: [],
};
export default config;
