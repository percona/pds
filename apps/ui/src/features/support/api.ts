import { requestJson } from "@/lib/api";
import type { CreateTicketInput, ReplyTicketInput, SupportTicket, TicketList } from "./types";

/**
 * The page asks for as many tickets as the adapter will answer with in one
 * read. There is no paging on the support page, so anything past this would be
 * a ticket nobody can reach; the adapter refuses a larger number.
 */
const ticketPageSize = 100;

/**
 * Reads the account's tickets. A body without the `tickets` field means there
 * is nothing to list rather than something to report, which is the same reading
 * the database list takes.
 */
export async function fetchTickets(signal?: AbortSignal): Promise<TicketList> {
  const query = new URLSearchParams({ limit: String(ticketPageSize) });
  const payload = await requestJson<Partial<TicketList>>(
    `/api/support/tickets?${query.toString()}`,
    { signal },
  );

  return {
    tickets: Array.isArray(payload?.tickets) ? payload.tickets : [],
    portalUrl: payload?.portalUrl,
  };
}

export function fetchTicket(ticketNumber: string, signal?: AbortSignal): Promise<SupportTicket> {
  return requestJson<SupportTicket>(`/api/support/tickets/${encodeURIComponent(ticketNumber)}`, {
    signal,
  });
}

export function createTicket(
  input: CreateTicketInput,
  signal?: AbortSignal,
): Promise<SupportTicket> {
  return requestJson<SupportTicket>("/api/support/tickets", {
    method: "POST",
    body: input,
    signal,
  });
}

export function replyToTicket(
  ticketNumber: string,
  input: ReplyTicketInput,
  signal?: AbortSignal,
): Promise<void> {
  return requestJson<void>(`/api/support/tickets/${encodeURIComponent(ticketNumber)}/replies`, {
    method: "POST",
    body: input,
    signal,
  });
}
