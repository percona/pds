import { requestJson } from "@/lib/api";
import { databaseResourcePath } from "@/features/databases/api";

export type HistoryEntry = {
  id: string;
  databaseId?: string;
  environment?: string;
  kind: string;
  at: string;
  action?: string;
  outcome?: string;
  detail?: string;
  error?: string;
  message?: string;
  fromPhase?: string;
  toPhase?: string;
  objectId?: string;
  sizeBytes?: number;
  durationMs?: number;
  reason?: string;
};

export type HistoryPage = {
  entries: HistoryEntry[];
  cursor?: string;
  truncated: boolean;
};

export type UsagePoint = {
  id: string;
  databaseId: string;
  at: string;
  cpuAvg?: number;
  cpuMax?: number;
  memoryAvg?: number;
  memoryMax?: number;
  storageBytes?: number;
  metricsReason?: string;
};

export type JoinPhase = {
  phase: string;
  outcome: string;
  durationMs: number;
  output: string;
};

export type JoinRun = {
  runId: string;
  phases: JoinPhase[];
};

export function fetchDatabaseHistory(id: string, signal?: AbortSignal): Promise<HistoryPage> {
  return requestJson<HistoryPage>(`/api/databases/${databaseResourcePath(id)}/history`, { signal });
}

export function fetchDatabaseUsage(
  id: string,
  signal?: AbortSignal,
): Promise<{ samples: UsagePoint[] }> {
  return requestJson<{ samples: UsagePoint[] }>(
    `/api/databases/${databaseResourcePath(id)}/usage`,
    { signal },
  );
}

export function fetchEnvironmentHistory(
  name: string,
  signal?: AbortSignal,
): Promise<{ runs: JoinRun[] }> {
  return requestJson<{ runs: JoinRun[] }>(`/api/environments/${encodeURIComponent(name)}/history`, {
    signal,
  });
}
