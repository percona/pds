import { ApiError, requestJson } from "@/lib/api";
import type { AgentSession } from "./types";

export async function fetchAgentSessions(): Promise<AgentSession[]> {
  const body = await requestJson<{ sessions: AgentSession[] }>("/api/agent-sessions");

  return body.sessions;
}

/**
 * Ends the session and every token it holds. An agent that is already gone is
 * what the person wanted, so a 404 is a success.
 */
export async function revokeAgentSession(id: string): Promise<void> {
  try {
    await requestJson<void>(`/api/agent-sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
  } catch (failure) {
    if (!(failure instanceof ApiError && failure.status === 404)) {
      throw failure;
    }
  }
}
