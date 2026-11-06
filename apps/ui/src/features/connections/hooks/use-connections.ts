import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchConnections } from "../api";
import type { ConnectionsStatus } from "../types";

export const connectionsKey = ["connections"] as const;

export function useConnections() {
  return useQuery<ConnectionsStatus, ApiError>({
    queryKey: connectionsKey,
    queryFn: ({ signal }) => fetchConnections(signal),
  });
}
