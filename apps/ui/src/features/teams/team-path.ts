/** A Team's page. Its name is free text, so it is encoded as a path segment. */
export function teamPath(name: string): string {
  return `/teams/${encodeURIComponent(name)}`;
}
