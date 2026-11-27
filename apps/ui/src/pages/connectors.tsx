import { PageHeader } from "@/components/layout/page-header";
import { ConnectionsPanel } from "@/features/connections";

/** Organization keys. Everyone can see which are connected. */
export function ConnectorsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Connectors"
        description="Keys for this organization. An admin can add or replace them."
      />
      <ConnectionsPanel />
    </div>
  );
}
