import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { teamsKey } from "@/features/teams";
import type { ApiError } from "@/lib/api";
import {
  createInvitation,
  fetchInvitations,
  fetchMembers,
  removeMember,
  resendInvitation,
  revokeInvitation,
  updateMember,
} from "../api";
import type {
  Invitation,
  InviteInput,
  InviteOutcome,
  IssuedInvitation,
  Member,
  MemberChange,
} from "../types";

const membersKey = ["users"] as const;
const invitationsKey = ["invitations"] as const;

export function useMembers() {
  return useQuery<Member[], ApiError>({ queryKey: membersKey, queryFn: fetchMembers });
}

export function useInvitations() {
  return useQuery<Invitation[], ApiError>({ queryKey: invitationsKey, queryFn: fetchInvitations });
}

/** Issuing, resending and revoking all change what is pending, so each is followed by a read. */
function useInvitationChange<TInput, TResult>(write: (input: TInput) => Promise<TResult>) {
  const queryClient = useQueryClient();

  return useMutation<TResult, ApiError, TInput>({
    mutationFn: write,
    onSuccess: () => queryClient.invalidateQueries({ queryKey: invitationsKey }),
  });
}

/**
 * Sends one invitation per address. The control plane takes one address a
 * request, so a refusal for one address is that address's outcome and the
 * others are still sent.
 */
async function inviteEach(inputs: InviteInput[]): Promise<InviteOutcome[]> {
  const settled = await Promise.allSettled(inputs.map((input) => createInvitation(input)));

  return settled.map((result, index) =>
    result.status === "fulfilled"
      ? { kind: "issued", invitation: result.value }
      : {
          kind: "refused",
          email: inputs[index]?.email ?? "",
          reason:
            result.reason instanceof Error ? result.reason.message : "The invite was not sent.",
        },
  );
}

export function useSendInvitations() {
  return useInvitationChange<InviteInput[], InviteOutcome[]>(inviteEach);
}

export function useResendInvitation() {
  return useInvitationChange<number, IssuedInvitation>(resendInvitation);
}

export function useRevokeInvitation() {
  return useInvitationChange<number, void>(revokeInvitation);
}

/** A User's Team and role also decide each Team's members, so the Teams are read again too. */
function useMemberChange<TInput>(write: (input: TInput) => Promise<void>) {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, TInput>({
    mutationFn: write,
    onSuccess: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: membersKey }),
        queryClient.invalidateQueries({ queryKey: teamsKey }),
      ]),
  });
}

export function useUpdateMember(id: number) {
  return useMemberChange((change: MemberChange) => updateMember(id, change));
}

export function useRemoveMember(id: number) {
  return useMemberChange(() => removeMember(id));
}
