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
