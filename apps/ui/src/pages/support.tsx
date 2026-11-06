import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { ContactSupportDialog, TicketList, useSupportAvailability } from "@/features/support";

export function SupportPage() {
  const [open, setOpen] = useState(false);
  const support = useSupportAvailability();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Support"
        description="Tickets this account has open with Percona support."
        actions={
          support.connected ? (
            <Button onClick={() => setOpen(true)}>Contact support</Button>
          ) : undefined
        }
      />

      <TicketList />

      <ContactSupportDialog
        open={open}
        onOpenChange={setOpen}
        subject="Support request"
        description="What is going wrong, and which database it is on."
        connected={support.connected}
        portalUrl={support.portalUrl}
      />
    </div>
  );
}
