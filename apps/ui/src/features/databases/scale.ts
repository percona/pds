import type { ScaleDatabaseInput } from "./types";

/** The fields a scale can change, in the order the dialog shows them. */
export const scaleFields = ["cpu", "memory", "storage", "replicas"] as const;

export type ScaleField = (typeof scaleFields)[number];

/**
 * The member counts the adapter accepts, mirrored from the engine plugin. A
 * Galera cluster decides quorum by majority, so it runs as one member or an odd
 * number of them: offering 2 would only earn a rejection.
 */
export const memberChoices = [1, 3, 5];

/** One database's sizing as the dialog reads and writes it. */
export type ScaleValues = Record<ScaleField, number>;

/**
 * Only the fields that actually moved travel to the adapter. Resending a value
 * that did not change would still be a spec write, and a spec write restarts
 * members.
 */
export function scaleChanges(current: ScaleValues, next: ScaleValues): ScaleDatabaseInput {
  const out: ScaleDatabaseInput = {};

  for (const field of scaleFields) {
    if (next[field] !== current[field]) {
      out[field] = next[field];
    }
  }

  return out;
}

/** What a member count buys, stated as the consequence of picking it. */
export function memberConsequence(members: number): string {
  switch (members) {
    case 1:
      return "One member. A restart is downtime.";
    case 3:
      return "Three members with HA. The database stays up while one restarts.";
    default:
      return "Five members with HA. The database stays up while two restart.";
  }
}
