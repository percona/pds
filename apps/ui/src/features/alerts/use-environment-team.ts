import { useEnvironments } from "@/features/environments";
import { useActingTeam } from "@/features/teams";

/**
 * The Team whose environment an alert link points at. While every Team is shown
 * the console still acts in the person's own Team, but the environment may
 * belong to another, so its own row names the owner. Two Teams with an
 * environment of the same name leave the acting Team when it is one of them, and
 * no Team otherwise, which the update page then refuses as ambiguous.
 */
export function useEnvironmentTeam(name: string): string | undefined {
  const { team: acting, allTeams } = useActingTeam();
  const { environments } = useEnvironments();

  if (!allTeams) {
    return acting || undefined;
  }
  const owners = environments
    .filter((environment) => environment.name === name && environment.team)
    .map((environment) => environment.team as string);
  if (owners.length === 1) {
    return owners[0];
  }

  return owners.length === 0 || owners.includes(acting) ? acting || undefined : undefined;
}
