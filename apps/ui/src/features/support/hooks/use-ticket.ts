import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchTicket } from "../api";
import type { SupportTicket } from "../types";

export function ticketKey(ticketNumber: string) {
  return ["support", "ticket", ticketNumber.trim().toUpperCase()] as const;
}

/**
 * Reads one ticket with its state and public comments. Support replies land
 * minutes to hours apart, so this re-reads on a mount rather than on a poll.
 */
export function useTicket(ticketNumber: string) {
  return useQuery<SupportTicket, ApiError>({
    queryKey: ticketKey(ticketNumber),
    queryFn: ({ signal }) => fetchTicket(ticketNumber, signal),
    enabled: ticketNumber !== "",
    retry: false,
  });
}
