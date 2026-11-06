import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { createDatabase } from "../api";
import type { CreateDatabaseInput, Database } from "../types";
import { databaseKey, databasesKey } from "./use-database";

/**
 * Provisions a database. The response seeds the detail cache so the
 * provisioning screen renders the first step without waiting for a poll.
 */
export function useCreateDatabase(team?: string) {
  const queryClient = useQueryClient();

  return useMutation<Database, ApiError, CreateDatabaseInput>({
    mutationFn: (input) => createDatabase(input, team),
    onSuccess: (database) => {
      queryClient.setQueryData(databaseKey(database.id), database);
      void queryClient.invalidateQueries({ queryKey: databasesKey });
    },
  });
}
