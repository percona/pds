import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { deleteDatabase } from "../api";
import { backupsKey } from "./use-backups";
import { databaseKey, databasesKey } from "./use-database";

/**
 * Deletes a database. The reads of a database that is gone are dropped rather
 * than invalidated: re-taking them would only produce a 404 for a page that
 * has already left for the list, which is invalidated so the row goes with it.
 */
export function useDeleteDatabase(id: string) {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, void>({
    mutationFn: () => deleteDatabase(id),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: databaseKey(id) });
      queryClient.removeQueries({ queryKey: backupsKey(id) });
      void queryClient.invalidateQueries({ queryKey: databasesKey });
    },
  });
}
