import { useQuery } from "@tanstack/react-query";
import { fetchEnvironmentHistory, fetchDatabaseHistory, fetchDatabaseUsage } from "./api";

export function useDatabaseHistory(databaseId: string) {
  return useQuery({
    queryKey: ["history", databaseId],
    queryFn: ({ signal }) => fetchDatabaseHistory(databaseId, signal),
    enabled: databaseId !== "",
  });
}

export function useDatabaseUsage(databaseId: string, enabled = true) {
  return useQuery({
    queryKey: ["usage", databaseId],
    queryFn: ({ signal }) => fetchDatabaseUsage(databaseId, signal),
    enabled: enabled && databaseId !== "",
  });
}

export function useEnvironmentHistory(name: string) {
  return useQuery({
    queryKey: ["environment-history", name],
    queryFn: ({ signal }) => fetchEnvironmentHistory(name, signal),
    enabled: name !== "",
  });
}
