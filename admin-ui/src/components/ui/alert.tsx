// Copied from crow-tech-landing components/ui/alert.tsx. Panel additions:
// warning/success variants; shadow colour from the --shadow token.
import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const alertVariants = cva(
  "relative w-full rounded-lg border-2 px-4 py-3 text-sm [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:left-4 [&>svg]:top-4 [&>svg]:text-foreground [&>svg~*]:pl-7",
  {
    variants: {
      variant: {
        default: "border-primary bg-background text-foreground shadow-brutal-xs",
        destructive:
          "border-destructive bg-background text-destructive [&>svg]:text-destructive [box-shadow:3px_3px_0px_0px_#C92929]",
        warning: "border-primary-400 bg-kuning-100 text-primary-400 [&>svg]:text-primary-400 shadow-brutal-xs",
        success: "border-primary-400 bg-success-50 text-primary-400 [&>svg]:text-primary-400 shadow-brutal-xs",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

const Alert = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement> & VariantProps<typeof alertVariants>
>(({ className, variant, role = "alert", ...props }, ref) => (
  <div ref={ref} role={role} className={cn(alertVariants({ variant }), className)} {...props} />
));
Alert.displayName = "Alert";

const AlertTitle = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLHeadingElement>>(
  ({ className, ...props }, ref) => (
    <h5 ref={ref} className={cn("mb-1 font-azeret font-bold leading-tight tracking-tight", className)} {...props} />
  ),
);
AlertTitle.displayName = "AlertTitle";

const AlertDescription = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLParagraphElement>>(
  ({ className, ...props }, ref) => <div ref={ref} className={cn("text-sm [&_p]:leading-relaxed", className)} {...props} />,
);
AlertDescription.displayName = "AlertDescription";

export { Alert, AlertTitle, AlertDescription };
