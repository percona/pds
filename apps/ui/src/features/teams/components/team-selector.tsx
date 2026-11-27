import { Users } from "lucide-react";
import { useIsAdmin } from "@/features/auth";
import { ALL_TEAMS } from "@/lib/acting-team";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { useActingTeam } from "../acting-team";

/**
 * Which Team the console is acting in. A User belongs to one Team, so they get
 * the name as a label, like the Organization above it. An admin gets a list of
 * every Team of the Organization to act in.
 */
export function TeamSelector({ compact = false }: { compact?: boolean }) {
  const admin = useIsAdmin();
  const { team, allTeams, teams, choose } = useActingTeam();

  if (team === "") {
    return null;
  }

  if (!admin || teams.length === 0) {
    return (
      <p
        className={cn(
          "flex items-center gap-2 px-2.5 py-2 text-sm font-medium",
          compact && "justify-center px-0",
        )}
        title="Team"
      >
        <Users className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        <span className={cn("truncate", compact && "sr-only")}>{team}</span>
      </p>
    );
  }

  if (compact) {
    return (
      <div className="relative flex justify-center" title={allTeams ? "All teams" : team}>
        <Users
          className="size-4 shrink-0 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <select
          aria-label="Working in team"
          value={allTeams ? ALL_TEAMS : team}
          onChange={(event) => choose(event.target.value)}
          className="absolute inset-0 cursor-pointer opacity-0"
        >
          <option value={ALL_TEAMS}>All teams</option>
          {teams.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-2 px-2.5">
      <Users
        className="size-4 shrink-0 text-muted-foreground"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <NativeSelect
        aria-label="Working in team"
        value={allTeams ? ALL_TEAMS : team}
        onChange={(event) => choose(event.target.value)}
        className="border-transparent bg-transparent font-medium"
      >
        <option value={ALL_TEAMS}>All teams</option>
        {teams.map((name) => (
          <option key={name} value={name}>
            {name}
          </option>
        ))}
      </NativeSelect>
    </div>
  );
}
