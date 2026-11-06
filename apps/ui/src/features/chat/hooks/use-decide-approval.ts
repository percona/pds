import { useMutation } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { decideApproval, type ApprovalDecision } from "../api";

/** Answers a change the Chat is waiting on. */
export function useDecideApproval() {
  return useMutation<void, ApiError, ApprovalDecision & { id: string }>({
    mutationFn: ({ id, ...decision }) => decideApproval(id, decision),
  });
}
