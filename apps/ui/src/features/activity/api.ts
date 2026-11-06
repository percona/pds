import { ApiError, requestJson } from "@/lib/api";
import type { ActivityDetail, ActivityPage } from "./types";

type ActivityQuery = {
  databaseId?: string;
  /** The cursor from the previous answer. Exclusive: only newer rows come back. */
  since?: string;
  limit?: number;
  signal?: AbortSignal;
};

/**
 * Reads a page of the activity ring.
 *
 * The adapter answers 501 when it was built without a ring, which the caller
 * treats as a deployment fact rather than a failure, the same way the support
 * routes behave.
 */
export async function fetchActivity({
  databaseId,
  since,
  limit,
  signal,
}: ActivityQuery = {}): Promise<ActivityPage> {
  const params = new URLSearchParams();
  if (databaseId) {
    params.set("databaseId", databaseId);
  }
  if (since) {
    params.set("since", since);
  }
  if (limit !== undefined) {
    params.set("limit", String(limit));
  }

  const query = params.toString();
  const payload = await requestJson<ActivityPage>(`/api/activity${query ? `?${query}` : ""}`, {
    signal,
  });

  if (!payload || !Array.isArray(payload.entries)) {
    throw new ApiError("ErrInternal", "The activity response could not be read.", 200);
  }

  return payload;
}

/**
 * Reads one recorded action with everything the platform can still say about
 * what it started.
 */
export async function fetchActivityDetail(
  id: string,
  signal?: AbortSignal,
): Promise<ActivityDetail> {
  const payload = await requestJson<ActivityDetail>(`/api/activity/${encodeURIComponent(id)}`, {
    signal,
  });

  if (!payload || !payload.event || !Array.isArray(payload.timeline)) {
    throw new ApiError("ErrInternal", "The activity detail could not be read.", 200);
  }

  return payload;
}
