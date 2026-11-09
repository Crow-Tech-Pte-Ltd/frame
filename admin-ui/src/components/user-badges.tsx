import { ShieldCheck } from "lucide-react";

import type { AdminUser } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";

export function UserBadges({ user, className }: { user: Pick<AdminUser, "is_admin" | "disabled_at">; className?: string }) {
  if (!user.is_admin && !user.disabled_at) return null;
  return (
    <span className={cn("inline-flex flex-wrap gap-1", className)}>
      {user.is_admin ? (
        <Badge variant="kuning">
          <ShieldCheck className="h-3 w-3" aria-hidden="true" />
          Admin
        </Badge>
      ) : null}
      {user.disabled_at ? <Badge variant="danger">Suspended</Badge> : null}
    </span>
  );
}

