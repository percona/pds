import { requestJson } from "@/lib/api";
import type { Team } from "./types";

type TeamsBody = { teams: Array<{ name: string }> };
type UsersBody = { users: Array<{ team: string }> };

/**
 * Reads the Teams with how many Users each holds. The control plane lists
 * Teams by name alone, so the counts come from the Users, read beside them.
 */
export async function fetchTeams(): Promise<Team[]> {
  const [teams, users] = await Promise.all([
    requestJson<TeamsBody>("/api/teams"),
    requestJson<UsersBody>("/api/users"),
  ]);

  return teams.teams.map(({ name }) => ({
    name,
    users: users.users.filter((user) => user.team === name).length,
  }));
}

export function createTeam(name: string): Promise<void> {
  return requestJson<void>("/api/teams", { method: "POST", body: { name } });
}

export function renameTeam(from: string, to: string): Promise<void> {
  return requestJson<void>(teamPath(from), { method: "PATCH", body: { name: to } });
}

export function deleteTeam(name: string): Promise<void> {
  return requestJson<void>(teamPath(name), { method: "DELETE" });
}

/** A Team's name is free text, so it is encoded wherever it sits in a path. */
function teamPath(name: string): string {
  return `/api/teams/${encodeURIComponent(name)}`;
}
