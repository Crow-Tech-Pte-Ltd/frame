// Copied from crow-tech-landing components/ui/badge.tsx. Panel additions:
// status variants (kuning/success/warning/muted) with fixed dark text.
import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-xs font-semibold font-azeret transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground shadow",
        secondary: "border-transparent bg-secondary text-secondary-foreground",
        danger: "border-transparent bg-destructive text-destructive-foreground shadow",
        outline: "text-foreground border-primary",
        kuning: "border-primary-400 bg-kuning-200 text-primary-400",
        success: "border-primary-400 bg-success-100 text-primary-400",
        warning: "border-primary-400 bg-warning-100 text-primary-400",
        info: "border-primary-400 bg-biru-50 text-primary-400",
        muted: "border-border bg-transparent text-muted-foreground",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement>, VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
