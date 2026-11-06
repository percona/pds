import type { Database } from "@/features/databases/types";
import type { TicketContextInput } from "./types";

/** Adapter vocabulary for topology: cluster when members > 1, else standalone. */
export function clusterTypeFromMembers(members: number): string {
  return members > 1 ? "cluster" : "standalone";
}

/**
 * The database facts POST /api/support/tickets expects. The adapter appends
 * them to the description and sanitizes the result.
 */
export function buildTicketContext(
  database: Database,
  options?: { errorMessage?: string },
): TicketContextInput {
  return {
    database: database.name,
    profile: database.profile,
    engine: database.engine,
    engineVersion: database.version,
    clusterType: clusterTypeFromMembers(database.members),
    namespace: database.namespace,
    members: database.members,
    status: database.status,
    errorMessage: options?.errorMessage,
  };
}

/** The error line for a failed create: event, operator message, or step label. */
export function provisioningErrorMessage(
  database: Database,
  failed?: { label: string; event?: string },
): string | undefined {
  if (failed?.event) {
    return failed.event;
  }

  if (database.message) {
    return database.message;
  }

  if (failed) {
    return `${failed.label} failed`;
  }

  return undefined;
}
