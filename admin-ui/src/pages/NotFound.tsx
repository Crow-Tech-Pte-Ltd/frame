import { Link } from "@/lib/router";
import { PageHeader } from "@/components/common";

export function NotFoundPage() {
  return (
    <div>
      <PageHeader title="Page not found" description="There's nothing at this address in the admin panel." />
      <Link to="/" className="font-azeret font-bold text-biru-400 underline underline-offset-4">
        Back to the overview
      </Link>
    </div>
  );
}
