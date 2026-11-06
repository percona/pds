import { useState, type ReactNode } from "react";
import { Field } from "@/components/ui/field";
import { NativeSelect } from "@/components/ui/native-select";
import { useActingTeam } from "./acting-team";

type TeamToCreateIn = {
  /** The Team to send with the write. Undefined unless every Team is shown. */
  team: string | undefined;
  /** The Team question to put in the flow. Null unless every Team is shown. */
  field: ReactNode;
};

/**
 * Which Team a create or a connect is for. While the console acts in one Team
 * that Team is the answer and nothing is asked. While an admin reads every
 * Team there is no single Team to act in, so the flow asks, starting from the
 * admin's own. A flow whose object already belongs to a Team passes it as
 * `fixed`: the write goes to that Team and nothing is asked, because any other
 * Team would not find the object.
 */
export function useTeamToCreateIn(fixed?: string): TeamToCreateIn {
  const { team: own, allTeams, teams } = useActingTeam();
  const [chosen, setChosen] = useState<string | null>(null);

  if (!allTeams) {
    return { team: undefined, field: null };
  }

  if (fixed !== undefined) {
    return { team: fixed, field: null };
  }

  const team = chosen !== null && teams.includes(chosen) ? chosen : own;

  return {
    team,
    field: (
      <Field id="team" label="Team" help="The Team this belongs to.">
        {(props) => (
          <NativeSelect {...props} value={team} onChange={(event) => setChosen(event.target.value)}>
            {teams.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </NativeSelect>
        )}
      </Field>
    ),
  };
}
