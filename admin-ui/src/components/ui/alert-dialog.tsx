// Adapted from crow-tech-landing components/ui/alert-dialog.tsx.
//
// Same exported names and look, but built on the native <dialog> element
// instead of @radix-ui/react-alert-dialog: Radix's scroll lock
// (react-remove-scroll) injects a <style> element at runtime, which the admin
// panel's CSP (`style-src 'self'`) blocks. showModal() gives us the top
// layer, an inert background, focus containment and Esc-to-close for free.
import * as React from "react";

import { cn } from "@/lib/utils";
import { buttonVariants } from "@/components/ui/button";

type Ctx = {
  open: boolean;
  setOpen: (open: boolean) => void;
  titleId: string;
  descriptionId: string;
};
const DialogContext = React.createContext<Ctx | null>(null);

function useDialog() {
  const ctx = React.useContext(DialogContext);
  if (!ctx) throw new Error("AlertDialog parts must be inside <AlertDialog>");
  return ctx;
}

let openCount = 0;
function lockScroll(lock: boolean) {
  openCount = Math.max(0, openCount + (lock ? 1 : -1));
  document.body.classList.toggle("dialog-open", openCount > 0);
}

interface AlertDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: React.ReactNode;
}

function AlertDialog({ open, onOpenChange, children }: AlertDialogProps) {
  const id = React.useId();
  const value = React.useMemo<Ctx>(
    () => ({ open, setOpen: onOpenChange, titleId: `${id}-title`, descriptionId: `${id}-desc` }),
    [open, onOpenChange, id],
  );
  return <DialogContext.Provider value={value}>{children}</DialogContext.Provider>;
}

interface ContentProps extends React.HTMLAttributes<HTMLDialogElement> {
  /** Close when the backdrop is clicked (drawers yes, confirmations no). */
  dismissOnBackdrop?: boolean;
  /** "dialog" = centred card (default), "drawer" = right-hand sheet. */
  layout?: "dialog" | "drawer";
}

const AlertDialogContent = React.forwardRef<HTMLDialogElement, ContentProps>(
  ({ className, children, dismissOnBackdrop = false, layout = "dialog", ...props }, forwardedRef) => {
    const { open, setOpen, titleId, descriptionId } = useDialog();
    const ref = React.useRef<HTMLDialogElement | null>(null);
    const returnFocus = React.useRef<HTMLElement | null>(null);

    React.useImperativeHandle(forwardedRef, () => ref.current as HTMLDialogElement);

    React.useEffect(() => {
      const el = ref.current;
      if (!el) return;
      if (open && !el.open) {
        returnFocus.current = document.activeElement as HTMLElement | null;
        el.showModal();
        lockScroll(true);
        return () => {
          lockScroll(false);
          if (el.open) el.close();
          const target = returnFocus.current;
          if (target && document.contains(target)) target.focus();
        };
      }
      return undefined;
    }, [open]);

    return (
      <dialog
        ref={ref}
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        // React re-dispatches the non-bubbling close/cancel events of a
        // nested <dialog> (e.g. a confirmation inside the user drawer) to
        // its React ancestors: only react to events of this dialog.
        onClose={(e) => {
          if (e.target !== ref.current) return;
          if (open) setOpen(false);
        }}
        onCancel={(e) => {
          if (e.target !== ref.current) return;
          // Esc: let React state drive the close so effects run once.
          e.preventDefault();
          setOpen(false);
        }}
        onClick={(e) => {
          if (dismissOnBackdrop && e.target === ref.current) setOpen(false);
        }}
        className={cn(
          layout === "dialog"
            ? "frame-dialog w-[calc(100%-2rem)] max-w-lg rounded-lg border-2 border-primary bg-background p-6 text-foreground shadow-brutal backdrop:bg-black/30"
            : "frame-drawer m-0 ml-auto h-dvh max-h-none w-full max-w-xl border-0 border-l-2 border-primary bg-background p-0 text-foreground sm:w-[min(36rem,100vw)]",
          className,
        )}
        {...props}
      >
        {open ? (layout === "dialog" ? <div className="grid grid-cols-[minmax(0,1fr)] gap-4">{children}</div> : children) : null}
      </dialog>
    );
  },
);
AlertDialogContent.displayName = "AlertDialogContent";

const AlertDialogHeader = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex flex-col space-y-2 text-left", className)} {...props} />
);
AlertDialogHeader.displayName = "AlertDialogHeader";

const AlertDialogFooter = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)} {...props} />
);
AlertDialogFooter.displayName = "AlertDialogFooter";

const AlertDialogTitle = React.forwardRef<HTMLHeadingElement, React.HTMLAttributes<HTMLHeadingElement>>(
  ({ className, ...props }, ref) => {
    const { titleId } = useDialog();
    return <h2 ref={ref} id={titleId} className={cn("font-azeret text-lg font-bold", className)} {...props} />;
  },
);
AlertDialogTitle.displayName = "AlertDialogTitle";

const AlertDialogDescription = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLParagraphElement>>(
  ({ className, ...props }, ref) => {
    const { descriptionId } = useDialog();
    return <p ref={ref} id={descriptionId} className={cn("text-sm text-muted-foreground", className)} {...props} />;
  },
);
AlertDialogDescription.displayName = "AlertDialogDescription";

const AlertDialogAction = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ className, type = "button", ...props }, ref) => (
    <button ref={ref} type={type} className={cn(buttonVariants(), className)} {...props} />
  ),
);
AlertDialogAction.displayName = "AlertDialogAction";

const AlertDialogCancel = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement>>(
  ({ className, onClick, children, ...props }, ref) => {
    const { setOpen } = useDialog();
    return (
      <button
        ref={ref}
        type="button"
        className={cn(buttonVariants({ variant: "outline" }), className)}
        onClick={(e) => {
          onClick?.(e);
          if (!e.defaultPrevented) setOpen(false);
        }}
        {...props}
      >
        {children ?? "Cancel"}
      </button>
    );
  },
);
AlertDialogCancel.displayName = "AlertDialogCancel";

export {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogFooter,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogAction,
  AlertDialogCancel,
};
