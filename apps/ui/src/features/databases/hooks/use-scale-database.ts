import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { scaleDatabase } from "../api";
import type { Database, ScaleDatabaseInput } from "../types";
import { databaseKey, databasesKey } from "./use-database";

/**
 * Scales a database. The adapter answers with the database as it now reads,
 * which seeds the detail cache so the header shows the new phase without
 * waiting for the next poll.
 */
export function useScaleDatabase(id: string) {
  const queryClient = useQueryClient();

  return useMutation<Database, ApiError, ScaleDatabaseInput>({
    mutationFn: (input) => scaleDatabase(id, input),
    onSuccess: (database) => {
      queryClient.setQueryData(databaseKey(database.id), database);
      void queryClient.invalidateQueries({ queryKey: databasesKey });
    },
  });
}
