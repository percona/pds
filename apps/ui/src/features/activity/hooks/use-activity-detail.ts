import { useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchActivityDetail } from "../api";
import { isOperationActive } from "../operation";
import type { ActivityDetail } from "../types";

/**
 * How often an open detail re-reads while its operation is still going. Slower
 * than the feed: a timeline grows a line every few seconds at most, and each
 * read costs a Kubernetes event list.
 */
export const activityDetailPollMs = 5_000;

export function activityDetailKey(id: string) {
  return ["activity", "detail", id] as const;
}

/**
 * One activity row expanded into a timeline.
 *
 * The poll follows the operation rather than the clock: a provision that is
 * still creating volumes keeps updating, and a backup that finished last week
 * is read once and left alone.
 */
export function useActivityDetail(id: string) {
  return useQuery<ActivityDetail, ApiError>({
    queryKey: activityDetailKey(id),
    queryFn: ({ signal }) => fetchActivityDetail(id, signal),
    enabled: id !== "",
    refetchInterval: (query) =>
      isOperationActive(query.state.data?.resource) ? activityDetailPollMs : false,
  });
}
