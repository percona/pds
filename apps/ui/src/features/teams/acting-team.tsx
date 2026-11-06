import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useNavigate } from "react-router";
import { scopedQueries, useSession } from "@/features/auth";
import { ALL_TEAMS, setActingTeam } from "@/lib/acting-team";
import { teamsKey, useTeams } from "./hooks/use-teams";

type ActingTeamValue = {
  /**
   * The Team the console acts in: the person's own until an admin chooses
   * another. While every Team is shown it stays the person's own, which is
   * where a create defaults to.
   */
  team: string;
  /** True while an admin reads every Team's lists at once. */
  allTeams: boolean;
  /** Every Team the person may act in. Empty until the read answers, and for a User. */
  teams: string[];
  choose: (name: string) => void;
};

const ActingTeamContext = createContext<ActingTeamValue | null>(null);

/** The Team choice is remembered in the browser, for each Organization on its own. */
function storageKey(organization: string): string {
  return `pds-team:${organization}`;
}

function readChoice(organization: string): string | null {
  try {
    return window.localStorage.getItem(storageKey(organization));
  } catch {
    return null;
  }
}

function writeChoice(organization: string, name: string | null): void {
  try {
    if (name === null) {
      window.localStorage.removeItem(storageKey(organization));
    } else {
      window.localStorage.setItem(storageKey(organization), name);
    }
  } catch {
    // The choice then lasts for this tab only, which is all a blocked store can give.
  }
}

/**
 * Decides which Team the console acts in and tells the request layer, so every
 * read and write below it names that Team. An admin may act in any Team of the
 * Organization and the choice is remembered. Everybody else has the one Team
 * they belong to, which the control plane uses when no Team is named.
 */
export function ActingTeamProvider({ children }: { children: ReactNode }) {
  const session = useSession().data;
  const current = session?.organizations?.find((organization) => organization.current);
  const organization = String(current?.id ?? session?.organization ?? "");

  // What was remembered for one Organization says nothing about the next.
  return (
    <ScopedActingTeam key={organization} organization={organization}>
      {children}
    </ScopedActingTeam>
  );
}

function ScopedActingTeam({
  organization,
  children,
}: {
  organization: string;
  children: ReactNode;
}) {
  const session = useSession().data;
  const own = session?.team ?? "";
  const admin = session?.role === "admin";
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const teams = useTeams(admin);
  const [choice, setChoice] = useState(() => (admin ? readChoice(organization) : null));

  const wanted = admin && choice !== null && choice !== own ? choice : null;
  // The remembered Team is checked against the Organization's Teams before any
  // screen asks for data, because a Team deleted since would answer 404.
  const everyTeam = wanted === ALL_TEAMS;
  const checking = wanted !== null && !everyTeam && teams.isPending;
  const acting = everyTeam
    ? ALL_TEAMS
    : wanted !== null && teams.data?.some((team) => team.name === wanted)
      ? wanted
      : null;

  // Only an answer that lacks the Team proves it is gone. A failed read says
  // nothing, so the choice is kept for the next load.
  const gone = wanted !== null && !everyTeam && teams.data !== undefined && acting === null;
  useEffect(() => {
    if (gone) {
      writeChoice(organization, null);
    }
  }, [gone, organization]);

  // A layout effect runs before the screens below fetch, so their first read
  // already carries the Team.
  useLayoutEffect(() => {
    setActingTeam(acting);

    return () => setActingTeam(null);
  }, [acting]);

  // What is on screen belongs to the Team the console just left, so it starts
  // again from home. The first answer is not a change: a reload keeps its page.
  const settled = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    if (checking) {
      return;
    }
    if (settled.current !== undefined && settled.current !== acting) {
      void queryClient.resetQueries({ predicate: scopedQueries(teamsKey) });
      void navigate("/");
    }
    settled.current = acting;
  }, [acting, checking, queryClient, navigate]);

  const value = useMemo<ActingTeamValue>(
    () => ({
      team: acting === null || everyTeam ? own : acting,
      allTeams: everyTeam,
      teams: teams.data?.map((team) => team.name) ?? [],
      choose: (name) => {
        const next = name === own ? null : name;
        writeChoice(organization, next);
        setChoice(next);
      },
    }),
    [acting, everyTeam, own, teams.data, organization],
  );

  return (
    <ActingTeamContext.Provider value={value}>
      {checking ? null : children}
    </ActingTeamContext.Provider>
  );
}

/** The Team the console acts in. Outside the provider it is the person's own, with no choice. */
export function useActingTeam(): ActingTeamValue {
  const value = useContext(ActingTeamContext);
  const own = useSession().data?.team ?? "";

  return value ?? { team: own, allTeams: false, teams: [], choose: () => undefined };
}
