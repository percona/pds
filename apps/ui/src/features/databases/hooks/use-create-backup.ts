import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { createBackup } from "../api";
import type { Backup } from "../types";
import { backupsKey } from "./use-backups";

/**
 * Starts an on-demand backup. The adapter answers with the backup it created,
 * which goes straight to the top of the cached list: the row is pending, so
 * the list starts polling and follows it to an outcome.
 */
export function useCreateBackup(id: string) {
  const queryClient = useQueryClient();

  return useMutation<Backup, ApiError, void>({
    mutationFn: () => createBackup(id),
    onSuccess: (backup) => {
      queryClient.setQueryData<Backup[]>(backupsKey(id), (current) => [
        backup,
        ...(current ?? []).filter((entry) => entry.id !== backup.id),
      ]);
    },
  });
}
