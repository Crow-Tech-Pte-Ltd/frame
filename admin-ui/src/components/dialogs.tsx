// Reusable confirmation and password-confirmation dialogs.
import * as React from "react";
import { Loader2 } from "lucide-react";

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FormError } from "@/components/common";

export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  destructive = false,
  confirmDisabled = false,
  onConfirm,
  children,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  destructive?: boolean;
  confirmDisabled?: boolean;
  /** Resolve to close; throw to show the error inside the dialog. */
  onConfirm: () => Promise<void>;
  children?: React.ReactNode;
}) {
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  React.useEffect(() => {
    if (open) {
      setError(null);
      setPending(false);
    }
  }, [open]);
  return (
    <AlertDialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {children}
        <FormError error={error} />
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
          <Button
            variant={destructive ? "destructive" : "default"}
            disabled={pending || confirmDisabled}
            onClick={async () => {
              setPending(true);
              setError(null);
              try {
                await onConfirm();
                onOpenChange(false);
              } catch (err) {
                setError(err);
              } finally {
                setPending(false);
              }
            }}
          >
            {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
            {confirmLabel}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Asks for the admin's password again before a sensitive action. */
export function PasswordDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  withCode = false,
  onSubmit,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  withCode?: boolean;
  onSubmit: (password: string, code: string) => Promise<void>;
}) {
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<unknown>(null);
  const id = React.useId();
  React.useEffect(() => {
    if (open) {
      setPassword("");
      setCode("");
      setError(null);
      setPending(false);
    }
  }, [open]);
  return (
    <AlertDialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <AlertDialogContent>
        <form
          className="grid gap-4"
          onSubmit={async (e) => {
            e.preventDefault();
            setPending(true);
            setError(null);
            try {
              await onSubmit(password, code.trim());
              onOpenChange(false);
            } catch (err) {
              setError(err);
            } finally {
              setPending(false);
            }
          }}
        >
          <AlertDialogHeader>
            <AlertDialogTitle>{title}</AlertDialogTitle>
            <AlertDialogDescription>{description}</AlertDialogDescription>
          </AlertDialogHeader>
          <Field id={id + "-pw"} label="Your password">
            <Input
              id={id + "-pw"}
              type="password"
              autoComplete="current-password"
              required
              autoFocus
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </Field>
          {withCode ? (
            <Field id={id + "-code"} label="Authenticator code or recovery code">
              <Input
                id={id + "-code"}
                inputMode="text"
                autoComplete="one-time-code"
                required
                value={code}
                onChange={(e) => setCode(e.target.value)}
                className="font-azeret tracking-widest"
              />
            </Field>
          ) : null}
          <FormError error={error} />
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>Cancel</AlertDialogCancel>
            <Button type="submit" disabled={pending || !password}>
              {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
              {confirmLabel}
            </Button>
          </AlertDialogFooter>
        </form>
      </AlertDialogContent>
    </AlertDialog>
  );
}
