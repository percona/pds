let acting: string | null = null;

/** The choice that reads every Team of the Organization at once. */
export const ALL_TEAMS = "all";

/**
 * Sets the Team the console acts in, or null for the signed in person's own.
 * ALL_TEAMS reads every Team on the lists that answer it, and leaves anything
 * else in the person's own Team. It is read when a request is built, so it has
 * to be set before the screen that asks for data renders.
 */
export function setActingTeam(name: string | null): void {
  acting = name;
}

// What belongs to the Organization or to the session rather than to a Team. The
// control plane answers these the same whichever Team is named, and a Team that
// no longer exists would turn them into 404s.
const organizationRoutes = [
  "/api/auth/",
  "/api/features",
  "/api/teams",
  "/api/users",
  "/api/invitations",
  "/api/organization",
];

// The only routes that answer team=all. Any other route refuses it.
const listRoutes = ["/api/environments", "/api/databases", "/api/activity"];

/**
 * Adds `team` to a path for a route that acts in a Team. A Team named for the
 * request wins, so a write can say which Team it is for. Otherwise the chosen
 * Team is sent, and every Team only on a list read.
 */
export function inActingTeam(path: string, method = "GET", team?: string): string {
  if (organizationRoutes.some((prefix) => path.startsWith(prefix))) {
    return path;
  }

  const name = team ?? (acting === ALL_TEAMS ? everyTeam(path, method) : acting);
  if (name === null || name === undefined) {
    return path;
  }

  return `${path}${path.includes("?") ? "&" : "?"}team=${encodeURIComponent(name)}`;
}

function everyTeam(path: string, method: string): string | null {
  const route = path.split("?")[0];

  return method === "GET" && listRoutes.includes(route) ? ALL_TEAMS : null;
}
