import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchAgentSessions, revokeAgentSession } from "../api";
import type { AgentSession } from "../types";

const agentSessionsKey = ["agent-sessions"] as const;

/** The agents the person may see: their own, or every member's for an admin. */
export function useAgentSessions() {
  return useQuery<AgentSession[], ApiError>({
    queryKey: agentSessionsKey,
    queryFn: fetchAgentSessions,
    retry: false,
  });
}

export function useRevokeAgentSession() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, string>({
    mutationFn: revokeAgentSession,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: agentSessionsKey }),
  });
}
