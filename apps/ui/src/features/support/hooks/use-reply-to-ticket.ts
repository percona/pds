import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { replyToTicket } from "../api";
import type { ReplyTicketInput } from "../types";
import { ticketKey } from "./use-ticket";

/** Posts a public reply and refreshes the ticket thread afterwards. */
export function useReplyToTicket(ticketNumber: string) {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, ReplyTicketInput>({
    mutationFn: (input) => replyToTicket(ticketNumber, input),
    retry: false,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ticketKey(ticketNumber) });
    },
  });
}
