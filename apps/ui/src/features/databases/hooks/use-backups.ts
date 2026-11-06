import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchBackups } from "../api";
import { isBackupActive } from "../backups";
import type { Backup } from "../types";

export function backupsKey(id: string) {
  return ["databases", "backups", id] as const;
}

/**
 * How often the list re-reads while a backup is running. A backup of a small
 * database finishes in under a minute, so five seconds keeps the row honest
 * without hammering the Kubernetes API behind the adapter.
 */
export const backupsPollMs = 5000;

/** Polls while any backup is still moving, and stops when none is. */
export function backupsRefetchInterval(backups?: Backup[]): number | false {
  if (!backups) {
    return backupsPollMs;
  }

  return backups.some(isBackupActive) ? backupsPollMs : false;
}

type UseBackupsOptions = {
  /** False while the Backups tab is closed, so a tab nobody opened costs nothing. */
  enabled: boolean;
};

export function useBackups(id: string, { enabled }: UseBackupsOptions) {
  return useQuery<Backup[], ApiError>({
    queryKey: backupsKey(id),
    queryFn: ({ signal }) => fetchBackups(id, signal),
    enabled,
    refetchInterval: (query) => backupsRefetchInterval(query.state.data),
  });
}
