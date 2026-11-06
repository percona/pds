import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { createTicket } from "../api";
import type { CreateTicketInput, SupportTicket } from "../types";
import { supportTicketsKey } from "./use-support-tickets";

/**
 * Opens a support ticket. The list is invalidated so the support page shows the
 * new case, and the mutation state is what the dialog renders: a failure keeps
 * the draft, because retyping a ticket after a 502 is the worst moment to lose
 * it.
 */
export function useCreateTicket() {
  const queryClient = useQueryClient();

  return useMutation<SupportTicket, ApiError, CreateTicketInput>({
    mutationFn: (input) => createTicket(input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: supportTicketsKey });
    },
  });
}
