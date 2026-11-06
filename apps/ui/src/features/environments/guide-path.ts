/**
 * Where a command guide for an environment lives. The namespace rides in the query
 * because a guide is read around a disconnect, when the environments list no
 * longer knows it.
 */
export function guidePath(environment: string, guide: string, namespace?: string): string {
  const path = `/environments/${encodeURIComponent(environment)}/${guide}`;
  if (!namespace) return path;

  return `${path}?${new URLSearchParams({ namespace }).toString()}`;
}
