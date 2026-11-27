import type { Role } from "@/features/auth";

/** A User of the Organization the admin acts in. */
export type Member = {
  id: number;
  name: string;
  email?: string;
  team: string;
  role: Role;
};

/** An invitation nobody has claimed yet. */
export type Invitation = {
  id: number;
  /** Absent when anyone holding the link may claim it. */
  email?: string;
  anyEmail: boolean;
  team: string;
  role: Role;
  expiresAt: string;
  createdAt: string;
};

/** What an invitation needs: an address or any email, and where the person lands. */
export type InviteInput = {
  email: string;
  anyEmail: boolean;
  team: string;
  role: Role;
};

/**
 * What creating or resending answers with. The link is the one time the
 * control plane shows it: only its hash is kept.
 */
export type IssuedInvitation = Invitation & {
  link: string;
  emailed: boolean;
};

/** What an admin changes about a User. A field left out is left alone. */
export type MemberChange = {
  team?: string;
  role?: Role;
};

/**
 * What became of one address in an invite: its invitation and link, or the
 * control plane's reason for refusing it.
 */
export type InviteOutcome =
  | { kind: "issued"; invitation: IssuedInvitation }
  | { kind: "refused"; email: string; reason: string };
