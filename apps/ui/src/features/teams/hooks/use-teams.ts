import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import type { Environment } from "@/features/environments";
import {
  createTeam,
  deleteTeam,
  fetchEnvironmentsOfEveryTeam,
  fetchTeams,
  renameTeam,
} from "../api";
import type { Team } from "../types";

export const teamsKey = ["teams"] as const;

/** `enabled` is false for a person who may not read the Teams, so no request is made. */
export function useTeams(enabled = true) {
  return useQuery<Team[], ApiError>({ queryKey: teamsKey, queryFn: fetchTeams, enabled });
}

/** Every Team's environments, each with the Team it belongs to. */
export function useTeamEnvironments() {
  return useQuery<Environment[], ApiError>({
    queryKey: [...teamsKey, "environments"],
    queryFn: fetchEnvironmentsOfEveryTeam,
  });
}

/** A write is followed by a read, because the counts and the names both moved. */
function useTeamWrite<TInput>(write: (input: TInput) => Promise<void>) {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, TInput>({
    mutationFn: write,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: teamsKey }),
  });
}

export function useCreateTeam() {
  return useTeamWrite(createTeam);
}

export function useRenameTeam(from: string) {
  return useTeamWrite((to: string) => renameTeam(from, to));
}

export function useDeleteTeam(name: string) {
  return useTeamWrite(() => deleteTeam(name));
}
