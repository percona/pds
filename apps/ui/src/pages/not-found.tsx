import { Link } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";

export function NotFoundPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Page not found" description="This route does not exist in the app." />
      <Button asChild variant="outline">
        <Link to="/environments">Go to environments</Link>
      </Button>
    </div>
  );
}
