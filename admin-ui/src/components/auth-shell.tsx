// Centered card layout for the sign-in and setup pages.
import * as React from "react";

import { Card } from "@/components/ui/card";
import { Logo, ThemeSwitch } from "@/components/layout";
import { PRIVACY_NOTE } from "@/components/common";

export function AuthShell({ title, subtitle, children }: { title: string; subtitle?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col bg-page">
      <header className="flex items-center justify-between gap-4 px-4 py-4 sm:px-8">
        <Logo className="h-9 sm:h-10" />
        <ThemeSwitch />
      </header>
      <main id="main" className="flex flex-1 items-start justify-center px-4 pb-12 pt-4 sm:items-center sm:pt-0">
        <Card className="w-full max-w-md p-6 sm:p-8">
          <p className="font-azeret text-[11px] font-bold uppercase tracking-wider text-muted-foreground">Frame admin</p>
          <h1 className="mt-1 font-azeret text-2xl font-bold tracking-tight">{title}</h1>
          {subtitle ? <div className="mt-2 text-sm text-muted-foreground">{subtitle}</div> : null}
          <div className="mt-6">{children}</div>
        </Card>
      </main>
      <footer className="px-4 pb-6 text-center text-xs text-muted-foreground">{PRIVACY_NOTE}</footer>
    </div>
  );
}
