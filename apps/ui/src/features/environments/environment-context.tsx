import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { createContext, useContext, useMemo, type ReactNode } from "react";
import { fetchEnvironmentSnapshot } from "./api";
import type { Environment } from "./types";

export const environmentsKey = ["environments", "list"] as const;
export const environmentsPollMs = 15_000;

type EnvironmentContextValue = {
  environments: Environment[];
  /** True once /api/environments has answered. A failure with no prior snapshot is not resolved. */
  resolved: boolean;
  isPending: boolean;
  /** Set when the environment read failed. */
  error: Error | null;
};

const EnvironmentContext = createContext<EnvironmentContextValue | null>(null);

export function EnvironmentProvider({ children }: { children: ReactNode }) {
  const query = useQuery({
    queryKey: environmentsKey,
    queryFn: ({ signal }) => fetchEnvironmentSnapshot(signal),
    // The list changes as environments join and drop, so keep asking. A failed
    // read is not a reason to stop.
    refetchInterval: environmentsPollMs,
    // A blip must not blank the list. Keeping the last answer means the pages
    // that read it stay put while the poll recovers.
    placeholderData: keepPreviousData,
  });
  const environments = query.data?.environments ?? [];
  const resolved = query.data != null;
  const isPending = query.isPending || !resolved;

  const value = useMemo<EnvironmentContextValue>(
    () => ({
      environments,
      resolved,
      isPending,
      error: query.error,
    }),
    [environments, resolved, isPending, query.error],
  );

  return <EnvironmentContext.Provider value={value}>{children}</EnvironmentContext.Provider>;
}

const inactive: EnvironmentContextValue = {
  environments: [],
  resolved: true,
  isPending: false,
  error: null,
};

export function useEnvironments(): EnvironmentContextValue {
  return useContext(EnvironmentContext) ?? inactive;
}
