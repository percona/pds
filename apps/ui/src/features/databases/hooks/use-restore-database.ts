import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { restoreDatabase } from "../api";
import type { RestoreAccepted, RestoreDatabaseInput } from "../types";
import { databaseKey, databasesKey } from "./use-database";

/**
 * Restores a database from one of its backups. The adapter accepts the request
 * rather than waiting it out, so the answer the developer needs is the phase
 * on the detail page: the read is invalidated and the detail poll carries the
 * database from Restoring back to Ready.
 */
export function useRestoreDatabase(id: string) {
  const queryClient = useQueryClient();

  return useMutation<RestoreAccepted, ApiError, RestoreDatabaseInput>({
    mutationFn: (input) => restoreDatabase(id, input),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: databaseKey(id) });
      void queryClient.invalidateQueries({ queryKey: databasesKey });
    },
  });
}
