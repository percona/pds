/** What a person may do in an Organization. */
export type Role = "user" | "admin";

/** An Organization a person belongs to, as the switcher lists it. */
export type OrganizationSummary = {
  id: number;
  name: string;
  role: Role;
  /** The one the session acts in. */
  current: boolean;
};

/**
 * Who the browser is signed in as. The Organization fields are absent on a
 * session that predates Organizations, and `organizations` is only present for
 * a person who signed in through an identity provider.
 */
export type Session = {
  username: string;
  user?: string;
  organization?: string;
  team?: string;
  role?: Role;
  organizations?: OrganizationSummary[];
};

/** What an invitation from an Organization admin says, with the address masked. */
export type OrganizationInvite = {
  organization: string;
  team: string;
  role: Role;
  /** Absent when anyone holding the link may claim it. */
  email?: string;
  anyEmail: boolean;
};

/**
 * What an invite link is for. A link from an admin names an Organization; one
 * from Percona names the username whose password it sets.
 */
export type InvitePreview = OrganizationInvite | { username: string };

/** How this control plane lets people sign in. */
export type SignInMethod = "oidc" | "password";

/** What the control plane offers for signing in. */
export type AuthMethods = {
  methods: SignInMethod[];
};

/** The identity providers the sign in page offers. */
export type Provider = "google" | "github";

/** What a sign in needs. The password goes straight to the adapter. */
export type SignInInput = {
  username: string;
  password: string;
};

/**
 * What claiming an invite needs: the token from the link and a new password.
 * A person invited to an Organization also chooses the login they sign in with.
 */
export type ClaimInput = {
  token: string;
  password: string;
  login?: string;
};
