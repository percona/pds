/**
 * Where an environment is updated in place. Two Teams can each have an
 * environment of the same name, so the link carries the Team when the row has one.
 */
export function updatePath(name: string, team?: string): string {
  const path = `/environments/${encodeURIComponent(name)}/update`;

  return team ? `${path}?team=${encodeURIComponent(team)}` : path;
}
