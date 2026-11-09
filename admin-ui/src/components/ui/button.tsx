// Copied from crow-tech-landing components/ui/button.tsx. Panel changes:
// hover colours that also work in dark mode (primary is white there), and a
// "danger-outline" variant for destructive secondary actions.
import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium font-azeret transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground font-bold shadow hover:bg-primary/80",
        secondary: "border border-primary bg-background shadow-sm font-bold hover:bg-soft",
        darksecondary:
          "bg-primary text-surface-200 font-bold shadow hover:bg-primary-300 hover:text-surface-50",
        destructive: "bg-destructive text-destructive-foreground font-bold shadow-sm hover:bg-danger-300",
        "danger-outline":
          "border border-destructive text-destructive bg-background font-bold shadow-sm hover:bg-danger-50 hover:text-danger-300",
        success: "bg-success-200 text-primary-400 font-bold shadow-sm hover:bg-success-100",
        warning: "bg-warning-200 text-primary-400 font-bold shadow-sm hover:bg-warning-100",
        outline: "border border-primary shadow-sm font-bold hover:bg-soft",
        ghost: "hover:bg-soft",
        link: "text-biru-400 font-bold underline-offset-4 hover:underline",
      },
      size: {
        default: "h-10 px-4 py-1.5",
        sm: "h-8 rounded-md px-3 py-1 text-xs",
        lg: "h-11 rounded-md px-8 py-2",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, type, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        className={cn(buttonVariants({ variant, size, className }))}
        ref={ref}
        type={asChild ? type : (type ?? "button")}
        {...props}
      />
    );
  },
);
Button.displayName = "Button";

export { Button, buttonVariants };
