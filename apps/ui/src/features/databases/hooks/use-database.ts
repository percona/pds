import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchDatabase } from "../api";
import { isProvisioningComplete } from "../provisioning";
import type { Database } from "../types";

// The list and the detail sit in sibling keys rather than nested ones, so
// invalidating the list after a create does not mark the detail the create
// just seeded as stale.
export const databasesKey = ["databases", "list"] as const;

export function databaseKey(id: string) {
  return ["databases", "detail", id] as const;
}

/**
 * How often the provisioning screen re-reads the status. A create takes
 * minutes, so five seconds is frequent enough to feel live without hammering
 * the Kubernetes API behind the adapter.
 */
export const provisioningPollMs = 5000;

/**
 * Polls while a create is still moving, and stops once the connection is
 * published.
 *
 * A failure keeps polling. The operator reports Failed while a reconcile
 * retries, and it clears those itself within a cycle, so a poll that stopped
 * here would leave the screen showing a failure the database recovered from
 * minutes earlier, for a create that in fact succeeded.
 */
export function provisioningRefetchInterval(database?: Database): number | false {
  if (!database) {
    return provisioningPollMs;
  }

  if (isProvisioningComplete(database)) {
    return false;
  }

  return provisioningPollMs;
}

export function useDatabase(id: string) {
  return useQuery<Database, ApiError>({
    queryKey: databaseKey(id),
    queryFn: ({ signal }) => fetchDatabase(id, signal),
    refetchInterval: (query) => provisioningRefetchInterval(query.state.data),
  });
}
