import { requestJson } from "@/lib/api";
import type { AwsAccount, AwsConnectCommand, AwsConnectStatus } from "./types";

export async function fetchAwsAccounts(signal?: AbortSignal): Promise<AwsAccount[]> {
  const payload = await requestJson<{ cloudConnections?: AwsAccount[] }>("/api/cloud-connections", {
    signal,
  });

  return Array.isArray(payload?.cloudConnections) ? payload.cloudConnections : [];
}

/** One account with the steps the connect command reported. */
export function fetchAwsAccount(id: string, signal?: AbortSignal): Promise<AwsAccount> {
  return requestJson<AwsAccount>(`/api/cloud-connections/${encodeURIComponent(id)}`, { signal });
}

/**
 * Makes the one command that connects an AWS account. It holds a token the
 * control plane shows only now, so nothing reads it back. The regions are always
 * named: a list of region names, or the wildcard for all regions.
 */
export function issueAwsConnectCommand(
  allowedRegions: string[],
  team?: string,
  signal?: AbortSignal,
): Promise<AwsConnectCommand> {
  return requestJson<AwsConnectCommand>("/api/cloud-connection-tokens", {
    method: "POST",
    body: { allowedRegions },
    team,
    signal,
  });
}

/** Where a connect command stands. It names the account once the command has found it. */
export function fetchAwsConnectStatus(id: string, signal?: AbortSignal): Promise<AwsConnectStatus> {
  return requestJson<AwsConnectStatus>(`/api/cloud-connection-tokens/${encodeURIComponent(id)}`, {
    signal,
  });
}

export function cancelAwsConnectCommand(id: string, signal?: AbortSignal): Promise<void> {
  return requestJson<void>(`/api/cloud-connection-tokens/${encodeURIComponent(id)}`, {
    method: "DELETE",
    signal,
  });
}

/** Forgets the account in PDS. The stack in the account stays. `confirm` is its alias, or its ID. */
export function removeAwsAccount(id: string, confirm: string, signal?: AbortSignal): Promise<void> {
  return requestJson<void>(`/api/cloud-connections/${encodeURIComponent(id)}`, {
    method: "DELETE",
    body: { confirm },
    signal,
  });
}
