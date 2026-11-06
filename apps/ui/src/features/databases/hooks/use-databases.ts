import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchDatabases } from "../api";
import { isSettled } from "../status";
import type { Database } from "../types";
import { databasesKey } from "./use-database";

/**
 * How often the list re-reads while something is still moving. The list is one
 * adapter call, and a create takes minutes, so five seconds keeps the pill
 * honest without hammering the Kubernetes API behind the adapter.
 */
export const databasesPollMs = 5000;

/** Polls while any database is still being worked on, and stops when none is. */
export function databasesRefetchInterval(databases?: Database[]): number | false {
  if (!databases) {
    return databasesPollMs;
  }

  return databases.every(isSettled) ? false : databasesPollMs;
}

export function useDatabases(environmentFilter?: string) {
  return useQuery<Database[], ApiError>({
    queryKey: [...databasesKey, environmentFilter ?? ""],
    queryFn: ({ signal }) => fetchDatabases(environmentFilter, signal),
    refetchInterval: (query) => databasesRefetchInterval(query.state.data),
  });
}
