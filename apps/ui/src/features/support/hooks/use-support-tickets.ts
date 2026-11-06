import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchTickets } from "../api";
import type { TicketList } from "../types";

export const supportTicketsKey = ["support", "tickets"] as const;

/**
 * Reads the account's tickets. The adapter asks ServiceNow for the cases this
 * control plane opened, so this is one request whatever the size of the
 * account.
 *
 * This is also how the UI learns whether support is connected at all: an environment
 * with no ServiceNow Secret answers 501, so a retry would only ask a second
 * time about a deployment fact.
 */
export function useSupportTickets() {
  return useQuery<TicketList, ApiError>({
    queryKey: supportTicketsKey,
    queryFn: ({ signal }) => fetchTickets(signal),
    retry: false,
  });
}
