import { PageHeader } from "@/components/layout/page-header";
import { AlertsView } from "@/features/alerts";

/** What is firing in every environment, on one page. */
export function AlertsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Alerts"
        description="What is firing in your databases, across every environment."
      />

      <AlertsView />
    </div>
  );
}
