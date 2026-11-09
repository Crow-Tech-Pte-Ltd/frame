import * as React from "react";
import { DatabaseBackup, Loader2, Sparkles } from "lucide-react";

import { useToast } from "@/app-context";
import { get, post } from "@/lib/api";
import { bytes, plural } from "@/lib/format";
import type { Overview } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FormError, PageHeader, Stat, useResource } from "@/components/common";

export function MaintenancePage() {
  const toast = useToast();
  const overview = useResource((signal) => get<Overview>("overview", { signal }), []);
  const [password, setPassword] = React.useState("");
  const [backupPending, setBackupPending] = React.useState(false);
  const [backupError, setBackupError] = React.useState<unknown>(null);
  const [backupURL, setBackupURL] = React.useState<string | null>(null);
  const [cleanPending, setCleanPending] = React.useState(false);
  const [cleanError, setCleanError] = React.useState<unknown>(null);
  const [cleanResult, setCleanResult] = React.useState<{ orphan_media_removed: number; expired_invites_removed: number } | null>(null);
  const linkRef = React.useRef<HTMLAnchorElement>(null);

  React.useEffect(() => {
    if (backupURL) linkRef.current?.click();
  }, [backupURL]);

  const o = overview.data;

  return (
    <>
      <PageHeader title="Maintenance" description="Backups and housekeeping. The server also cleans up by itself every 10 minutes." />
      {o ? (
        <div className="mb-8 grid grid-cols-2 gap-4 md:grid-cols-3">
          <Stat label="Database" value={bytes(o.database.bytes)} />
          <Stat label="Media" value={bytes(o.storage.media_bytes)} hint={plural(o.storage.media_files, "file")} />
          <Stat label="Media storage" value={o.server.storage === "s3" ? "S3" : "Local"} />
        </div>
      ) : null}

      <div className="grid gap-6 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Database backup</CardTitle>
            <CardDescription>
              Downloads a consistent copy of the database (accounts, friendships, moments' metadata, messages) — taken
              live, no downtime.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Alert className="mb-4" role="note">
              <AlertDescription>
                The database doesn't contain the photos and videos themselves. Also back up{" "}
                <code className="font-azeret text-xs">secrets.json</code> and <code className="font-azeret text-xs">media/</code> from
                the data directory (or your S3 bucket) — see the self-hosting guide.
              </AlertDescription>
            </Alert>
            <form
              className="grid gap-4"
              onSubmit={async (e) => {
                e.preventDefault();
                setBackupPending(true);
                setBackupError(null);
                setBackupURL(null);
                try {
                  const r = await post<{ url: string; expires_at: number }>("maintenance/backup", { password });
                  setPassword("");
                  setBackupURL(r.url);
                  toast("Backup ready — your download should start.");
                } catch (err) {
                  setBackupError(err);
                } finally {
                  setBackupPending(false);
                }
              }}
            >
              <Field id="backup-password" label="Your password" hint="Asked again because a backup contains everyone's data.">
                <Input
                  id="backup-password"
                  type="password"
                  autoComplete="current-password"
                  required
                  aria-describedby="backup-password-hint"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <FormError error={backupError} />
              <Button type="submit" disabled={backupPending || !password} className="justify-self-start">
                {backupPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <DatabaseBackup aria-hidden="true" />}
                Download backup
              </Button>
              {backupURL ? (
                <p className="text-sm" role="status">
                  Download didn't start?{" "}
                  <a ref={linkRef} href={backupURL} download className="font-bold text-biru-400 underline underline-offset-2">
                    Download the backup
                  </a>{" "}
                  (the link works once, for 2 minutes).
                </p>
              ) : null}
            </form>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Clean up now</CardTitle>
            <CardDescription>
              Deletes media files no moment or avatar uses any more (older than an hour, e.g. from interrupted uploads)
              and expired invite links.
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4">
            <FormError error={cleanError} />
            {cleanResult ? (
              <p className="text-sm" role="status">
                Removed {plural(cleanResult.orphan_media_removed, "unused media file")} and{" "}
                {plural(cleanResult.expired_invites_removed, "expired invite")}.
              </p>
            ) : null}
            <Button
              variant="outline"
              className="justify-self-start"
              disabled={cleanPending}
              onClick={async () => {
                setCleanPending(true);
                setCleanError(null);
                try {
                  const r = await post<{ orphan_media_removed: number; expired_invites_removed: number }>("maintenance/cleanup");
                  setCleanResult(r);
                  overview.reload();
                } catch (err) {
                  setCleanError(err);
                } finally {
                  setCleanPending(false);
                }
              }}
            >
              {cleanPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Sparkles aria-hidden="true" />}
              Run cleanup
            </Button>
          </CardContent>
        </Card>
      </div>
    </>
  );
}
