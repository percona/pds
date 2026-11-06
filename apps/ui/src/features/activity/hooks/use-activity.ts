import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import type { ApiError } from "@/lib/api";
import { fetchActivity } from "../api";
import { mergeActivity } from "../merge";
import type { ActivityPage } from "../types";

/**
 * How often the feed re-reads. This is the screen a person watches while a
 * restore runs, and a steady state poll costs one empty response because the
 * cursor filters out everything already on screen.
 */
export const activityPollMs = 3_000;

/** The cache key. The database filter is part of it, so two views never
 * overwrite each other's answer. */
export function activityKey(databaseId?: string) {
  return ["activity", databaseId ?? "all"] as const;
}

type UseActivityOptions = {
  databaseId?: string;
  /** False stops the poll, which is what the Live toggle turns off. */
  live?: boolean;
};

/**
 * The activity feed.
 *
 * Polls are incremental, so what is cached is the running total rather than
 * the last answer: the rows and the cursor that says which rows have been seen
 * have to live in the same place. Keeping the rows in component state instead
 * puts them on different lifetimes, and leaving the page then empties the feed,
 * because the cursor survives the unmount and the rows do not, and the next
 * poll dutifully asks only for what arrived after rows it no longer has.
 *
 * The filter is part of the key, so changing it accumulates separately rather
 * than needing a reset.
 */
export function useActivity({ databaseId, live = true }: UseActivityOptions = {}) {
  const queryClient = useQueryClient();

  const query = useQuery<ActivityPage, ApiError>({
    queryKey: activityKey(databaseId),
    queryFn: async ({ signal }) => {
      const seen = queryClient.getQueryData<ActivityPage>(activityKey(databaseId));
      const page = await fetchActivity({
        databaseId,
        since: seen?.cursor || undefined,
        signal,
      });

      return {
        entries: mergeActivity(seen?.entries ?? [], page.entries),
        cursor: page.cursor,
        truncated: page.truncated,
      };
    },
    refetchInterval: live ? activityPollMs : false,
  });

  // A manual retry starts over: an error may have lost rows the cursor has
  // already stepped past, so the cursor goes with the rows it belonged to.
  const retry = useCallback(async () => {
    await queryClient.resetQueries({ queryKey: activityKey(databaseId) });
  }, [queryClient, databaseId]);

  return {
    entries: query.data?.entries ?? [],
    truncated: query.data?.truncated ?? false,
    error: query.error,
    isPending: query.isPending,
    isFetching: query.isFetching,
    retry,
  };
}
