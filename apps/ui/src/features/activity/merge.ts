import type { ActivityEntry } from "./types";

/**
 * How many rows the feed keeps in the browser. It matches the ring the adapter
 * stores, so scrolling back never reaches further than the adapter can answer.
 */
export const maxActivityEntries = 500;

/**
 * Folds a poll into what is already on screen.
 *
 * Polls are incremental: each one asks for what arrived after the last cursor,
 * so the answer has to be prepended rather than swapped in. Ids are zero padded
 * sequences, which is what lets a plain string compare put them in order, and
 * the dedupe covers the overlap when a filter change and a poll race.
 */
export function mergeActivity(
  existing: ActivityEntry[],
  incoming: ActivityEntry[],
  cap: number = maxActivityEntries,
): ActivityEntry[] {
  if (incoming.length === 0) {
    return existing.length > cap ? existing.slice(0, cap) : existing;
  }

  const byId = new Map<string, ActivityEntry>();
  for (const entry of existing) {
    byId.set(entry.id, entry);
  }
  // Incoming wins a collision: it is the fresher read of the same row.
  for (const entry of incoming) {
    byId.set(entry.id, entry);
  }

  const merged = [...byId.values()].toSorted((a, b) => (a.id < b.id ? 1 : a.id > b.id ? -1 : 0));

  return merged.slice(0, cap);
}
