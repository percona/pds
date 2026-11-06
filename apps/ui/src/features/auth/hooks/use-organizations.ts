import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import {
  createOrganization,
  fetchAuthMethods,
  renameOrganization,
  switchOrganization,
} from "../api";
import { dropScopedQueries, sessionKey } from "./use-session";

const authMethodsQuery = {
  queryKey: ["auth", "methods"],
  queryFn: fetchAuthMethods,
  retry: false,
  staleTime: Infinity,
};

/**
 * Which ways of signing in the control plane offers. A failed read is not
 * retried and leaves the password form up: that form is the one every
 * deployment serves, so it is the safe thing to show when nothing is known.
 */
export function useSignInMethods() {
  return useQuery({ ...authMethodsQuery, select: (offered) => offered.methods });
}

/**
 * Drops what was cached for the Organization the session just left and reads
 * the session again, which now names the new one. The session and the flag set
 * stay: neither belongs to an Organization.
 */
async function leaveOrganization(queryClient: QueryClient): Promise<void> {
  dropScopedQueries(queryClient);
  await queryClient.invalidateQueries({ queryKey: sessionKey });
}

/** Moves the session into another of the person's Organizations. */
export function useSwitchOrganization() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (organizationId: number) => switchOrganization(organizationId),
    onSuccess: () => leaveOrganization(queryClient),
  });
}

/** Makes another Organization and moves the session into it. */
export function useCreateOrganization() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (name: string) => createOrganization(name),
    onSuccess: () => leaveOrganization(queryClient),
  });
}

/**
 * Renames the Organization the session acts in. The session names it, and so
 * does the switcher, so the session is read again.
 */
export function useRenameOrganization() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, string>({
    mutationFn: (name: string) => renameOrganization(name),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: sessionKey }),
  });
}
