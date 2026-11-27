import { PageHeader } from "@/components/layout/page-header";
import { ContactSupportButton, TicketList, useSupportAvailability } from "@/features/support";

export function SupportPage() {
  const support = useSupportAvailability();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Support"
        description="Your cases with Percona support, from ServiceNow."
        actions={support.connected ? <ContactSupportButton variant="default" /> : undefined}
      />

      <TicketList />
    </div>
  );
}
