import { requestJson } from "@/lib/api";
import type {
  AssistantInput,
  ConnectionKind,
  ConnectionsStatus,
  CursorInput,
  ServiceNowInput,
} from "./types";

export async function fetchConnections(signal?: AbortSignal): Promise<ConnectionsStatus> {
  const payload = await requestJson<Partial<ConnectionsStatus>>("/api/connections", { signal });

  return {
    servicenow: { configured: payload.servicenow?.configured === true },
    assistant: { configured: payload.assistant?.configured === true },
    cursor: { configured: payload.cursor?.configured === true },
  };
}

export function putServiceNow(input: ServiceNowInput, signal?: AbortSignal): Promise<void> {
  return requestJson<void>("/api/connections/servicenow", {
    method: "PUT",
    body: input,
    signal,
  });
}

export function putAssistant(input: AssistantInput, signal?: AbortSignal): Promise<void> {
  return requestJson<void>("/api/connections/assistant", {
    method: "PUT",
    body: input,
    signal,
  });
}

export function putCursor(input: CursorInput, signal?: AbortSignal): Promise<void> {
  return requestJson<void>("/api/connections/cursor", {
    method: "PUT",
    body: input,
    signal,
  });
}

export function deleteConnection(kind: ConnectionKind, signal?: AbortSignal): Promise<void> {
  return requestJson<void>(`/api/connections/${kind}`, {
    method: "DELETE",
    signal,
  });
}
