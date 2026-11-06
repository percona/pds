import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { claimInvite, fetchInvite } from "../api";
import type { ClaimInput } from "../types";
import { forgetSignedInState } from "./use-session";

/** Reads what an invite link is for. */
export function useInvite(token: string) {
  return useQuery({
    queryKey: ["auth", "invite", token],
    queryFn: () => fetchInvite(token),
    enabled: token !== "",
    retry: false,
    staleTime: Infinity,
  });
}

/**
 * Claims the link. It deliberately does not seed the session: the customer
 * signs in afterwards with the password they just chose, which is the one
 * thing worth proving works before they are let in.
 *
 * A leftover session in this browser is dropped the same way a sign out is.
 * The adapter also clears the cookie; this is what stops the sign in page
 * bouncing them back in as whoever was already here.
 */
export function useClaimInvite() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: ClaimInput) => claimInvite(input),
    onSuccess: () => {
      forgetSignedInState(queryClient);
    },
  });
}
