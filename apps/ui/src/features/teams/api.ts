import type { Environment } from "@/features/environments";
import { ALL_TEAMS } from "@/lib/acting-team";
import { requestJson } from "@/lib/api";
import type { Team } from "./types";

type TeamsBody = { teams: Array<{ name: string }> };
type UsersBody = { users: Array<{ name: string; team: string }> };

/**
 * Reads the Teams with the Users each holds. The control plane lists Teams by
 * name alone, so the members come from the Users, read beside them.
 */
export async function fetchTeams(): Promise<Team[]> {
  const [teams, users] = await Promise.all([
    requestJson<TeamsBody>("/api/teams"),
    requestJson<UsersBody>("/api/users"),
  ]);

  return teams.teams.map(({ name }) => ({
    name,
    members: users.users.filter((user) => user.team === name).map((user) => user.name),
  }));
}

/**
 * Reads the environments of every Team at once, whichever Team the console
 * acts in, because only that list names the Team each one belongs to.
 */
export async function fetchEnvironmentsOfEveryTeam(): Promise<Environment[]> {
  const body = await requestJson<{ environments?: Environment[] }>("/api/environments", {
    team: ALL_TEAMS,
  });

  return Array.isArray(body?.environments) ? body.environments : [];
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
