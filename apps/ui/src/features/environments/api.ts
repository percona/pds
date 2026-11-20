import { requestJson } from "@/lib/api";
import type {
  Environment,
  EnvironmentSnapshot,
  ConnectGuide,
  JoinToken,
  ManagedCreated,
  Monitoring,
  MonitoringSettings,
  RunStarted,
} from "./types";

/** Reads the connected environments. A failed read is an error. */
export async function fetchEnvironmentSnapshot(signal?: AbortSignal): Promise<EnvironmentSnapshot> {
  const payload = await requestJson<Partial<EnvironmentSnapshot>>("/api/environments", { signal });
  const environments = Array.isArray(payload?.environments) ? payload.environments : [];

  return { environments };
}

export async function fetchEnvironments(signal?: AbortSignal): Promise<Environment[]> {
  return (await fetchEnvironmentSnapshot(signal)).environments;
}

/** Values the connect page needs before it can offer a join command. */
export async function fetchConnectGuide(signal?: AbortSignal): Promise<ConnectGuide> {
  const payload = await requestJson<Partial<ConnectGuide>>("/api/connect", { signal });

  return {
    hubUrl: payload?.hubUrl,
    gitUrl: payload?.gitUrl,
    githubOrg: payload?.githubOrg,
    namespace: payload?.namespace,
    clusterRegion: payload?.clusterRegion,
  };
}

/**
 * Mints a per-environment join token and the one command the spoke runs.
 * `team` names the Team the environment joins, when it is not the one the
 * console acts in.
 */
export function issueJoinToken(
  name: string,
  team?: string,
  signal?: AbortSignal,
): Promise<JoinToken> {
  return requestJson<JoinToken>("/api/connect/tokens", {
    method: "POST",
    body: { name },
    team,
    signal,
  });
}

/**
 * Removes an environment from the hub: its inventory row and its tokens. Desired
 * state stays in Git so a later join of the same name cannot prune databases
 * that were left running. The environment itself is untouched.
 */
export function disconnectEnvironment(name: string, signal?: AbortSignal): Promise<void> {
  return requestJson<void>(`/api/environments/${encodeURIComponent(name)}`, {
    method: "DELETE",
    signal,
  });
}

export function fetchMonitoring(name: string, signal?: AbortSignal): Promise<Monitoring> {
  return requestJson<Monitoring>(`/api/environments/${encodeURIComponent(name)}/monitoring`, {
    signal,
  });
}

/**
 * Reads the monitoring admin password through the environment's agent. It runs on
 * the copy click and nowhere else, and the value goes straight to the
 * clipboard: it is never held in state, never rendered and never logged.
 */
export async function fetchMonitoringPassword(name: string, signal?: AbortSignal): Promise<string> {
  const payload = await requestJson<{ password?: string }>(
    `/api/environments/${encodeURIComponent(name)}/monitoring/password`,
    { signal },
  );

  return typeof payload?.password === "string" ? payload.password : "";
}

/** Replaces an environment's monitoring settings. The environment applies them on its own. */
export function putMonitoring(
  name: string,
  settings: MonitoringSettings,
  signal?: AbortSignal,
): Promise<Monitoring> {
  return requestJson<Monitoring>(`/api/environments/${encodeURIComponent(name)}/monitoring`, {
    method: "PUT",
    body: settings,
    signal,
  });
}

/**
 * Asks PDS to build an environment in one of the Team's AWS accounts. The answer
 * comes before anything exists in AWS: the environment is already creating and
 * its run is queued.
 */
export function createManagedEnvironment(
  connectionId: string,
  input: { name: string; region: string },
  team?: string,
  signal?: AbortSignal,
): Promise<ManagedCreated> {
  return requestJson<ManagedCreated>(
    `/api/cloud-connections/${encodeURIComponent(connectionId)}/environments`,
    { method: "POST", body: input, team, signal },
  );
}

/** Re-runs the step of a run that failed. Nothing before it runs again. */
export function retryRun(name: string, runId: number, signal?: AbortSignal): Promise<RunStarted> {
  return requestJson<RunStarted>(
    `/api/environments/${encodeURIComponent(name)}/runs/${runId}/retry`,
    { method: "POST", signal },
  );
}

/**
 * Deletes a managed environment and what PDS built for it in AWS, after its
 * typed name. With `pdsOnly` it forgets the environment in PDS and leaves
 * everything in the AWS account, which is for an account PDS can no longer reach.
 */
export async function deleteManagedEnvironment(
  name: string,
  confirm: string,
  options: { pdsOnly?: boolean; signal?: AbortSignal } = {},
): Promise<void> {
  await requestJson<RunStarted | undefined>(`/api/environments/${encodeURIComponent(name)}`, {
    method: "DELETE",
    body: options.pdsOnly ? { confirm, scope: "pds_only" } : { confirm },
    signal: options.signal,
  });
}
