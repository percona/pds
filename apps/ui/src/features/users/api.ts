import { requestJson } from "@/lib/api";
import type { Invitation, InviteInput, IssuedInvitation, Member } from "./types";

export async function fetchMembers(): Promise<Member[]> {
  const body = await requestJson<{ users: Member[] }>("/api/users");

  return body.users;
}

export async function fetchInvitations(): Promise<Invitation[]> {
  const body = await requestJson<{ invitations: Invitation[] }>("/api/invitations");

  return body.invitations;
}

export function createInvitation(input: InviteInput): Promise<IssuedInvitation> {
  return requestJson<IssuedInvitation>("/api/invitations", { method: "POST", body: input });
}

/** Resending issues a new link, because the old one cannot be read back. */
export function resendInvitation(id: number): Promise<IssuedInvitation> {
  return requestJson<IssuedInvitation>(`/api/invitations/${id}/resend`, { method: "POST" });
}

export function revokeInvitation(id: number): Promise<void> {
  return requestJson<void>(`/api/invitations/${id}`, { method: "DELETE" });
}
