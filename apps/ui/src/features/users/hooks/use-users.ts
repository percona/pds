import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import {
  createInvitation,
  fetchInvitations,
  fetchMembers,
  resendInvitation,
  revokeInvitation,
} from "../api";
import type { Invitation, InviteInput, IssuedInvitation, Member } from "../types";

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

export function useCreateInvitation() {
  return useInvitationChange<InviteInput, IssuedInvitation>(createInvitation);
}

export function useResendInvitation() {
  return useInvitationChange<number, IssuedInvitation>(resendInvitation);
}

export function useRevokeInvitation() {
  return useInvitationChange<number, void>(revokeInvitation);
}
