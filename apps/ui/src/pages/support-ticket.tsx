import { useParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { TicketDetail } from "@/features/support";

export function SupportTicketPage() {
  const { number = "" } = useParams();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Ticket"
        description="What Percona support has, and what they have said about it."
      />

      <TicketDetail ticketNumber={number} />
    </div>
  );
}
