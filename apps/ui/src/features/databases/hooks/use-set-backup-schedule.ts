import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { setBackupSchedule } from "../api";
import type { BackupScheduleInput, Database } from "../types";
import { databaseKey, databasesKey } from "./use-database";

/**
 * Writes the backup schedule. No input turns on the default daily schedule,
 * which is what a chat action sends. The adapter answers with the database
 * carrying the schedule it wrote, which seeds the detail cache so the Backups
 * tab catches up without waiting for the next poll.
 */
export function useSetBackupSchedule(id: string) {
  const queryClient = useQueryClient();

  return useMutation<Database, ApiError, BackupScheduleInput | undefined>({
    mutationFn: (input) => setBackupSchedule(id, input),
    onSuccess: (database) => {
      queryClient.setQueryData(databaseKey(database.id), database);
      void queryClient.invalidateQueries({ queryKey: databasesKey });
    },
  });
}
