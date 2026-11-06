import { requestJson } from "@/lib/api";
import type { SupportOpenInput, SupportWindow } from "./types";

function supportAccessPath(environment: string): string {
  return `/api/environments/${encodeURIComponent(environment)}/support-access`;
}

/** Reads an environment's current window, or null when it has never had one. */
export async function fetchSupportAccess(
  environment: string,
  signal?: AbortSignal,
): Promise<SupportWindow | null> {
  const payload = await requestJson<{ window?: SupportWindow | null }>(
    supportAccessPath(environment),
    { signal },
  );

  return payload?.window ?? null;
}

/** Opens a window. It starts opening and is open once the environment says so. */
export async function openSupportAccess(
  environment: string,
  input: SupportOpenInput,
): Promise<SupportWindow> {
  const payload = await requestJson<{ window: SupportWindow }>(supportAccessPath(environment), {
    method: "POST",
    body: input,
  });

  return payload.window;
}

/** Ends a window. Ending one that is already closed succeeds. */
export function endSupportAccess(environment: string): Promise<void> {
  return requestJson<void>(supportAccessPath(environment), { method: "DELETE" });
}

/**
 * Asks for an open window to end `hours` from now. The window keeps its expiry
 * and carries `extendingTo` until the environment confirms.
 */
export async function extendSupportAccess(
  environment: string,
  hours: number,
): Promise<SupportWindow> {
  const payload = await requestJson<{ window: SupportWindow }>(supportAccessPath(environment), {
    method: "PATCH",
    body: { hours },
  });

  return payload.window;
}
