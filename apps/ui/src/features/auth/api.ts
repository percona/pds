import { ApiError, requestJson } from "@/lib/api";
import type {
  AuthMethods,
  ClaimInput,
  InvitePreview,
  Session,
  SignInInput,
  SignInMethod,
} from "./types";

/**
 * Reads who the browser is signed in as, or null when it is not.
 *
 * A 401 is an answer rather than a failure here: it is how the adapter says
 * nobody is signed in, and the gate around the app needs to tell that apart
 * from an adapter it cannot reach.
 */
export async function fetchSession(): Promise<Session | null> {
  try {
    return await requestJson<Session>("/api/auth/session");
  } catch (error) {
    if (error instanceof ApiError && error.status === 401) {
      return null;
    }

    throw error;
  }
}

/** Signs in. The adapter answers with a cookie the browser cannot read. */
export function signIn(input: SignInInput): Promise<Session> {
  return requestJson<Session>("/api/auth/login", { method: "POST", body: input });
}

/** Signs out, which deletes the session row rather than only the cookie. */
export function signOut(): Promise<void> {
  return requestJson<void>("/api/auth/logout", { method: "POST" });
}

/**
 * Reads what an invite link is for, so the page can name it. It does not spend
 * the link, so opening the page twice is fine.
 */
export function fetchInvite(token: string): Promise<InvitePreview> {
  return requestJson<InvitePreview>(`/api/auth/invite?token=${encodeURIComponent(token)}`);
}

/** Spends the invite link and sets the password the customer chose. */
export function claimInvite(input: ClaimInput): Promise<Session> {
  return requestJson<Session>("/api/auth/invite", { method: "POST", body: input });
}

/** Reads which ways of signing in this control plane offers, the provider first. */
export async function fetchAuthMethods(): Promise<AuthMethods> {
  const body = await requestJson<{ methods?: SignInMethod[] }>("/api/auth/methods");

  return { methods: body.methods ?? [] };
}

/** Moves the session into another Organization the person belongs to. */
export function switchOrganization(organizationId: number): Promise<void> {
  return requestJson<void>("/api/auth/organization", { method: "POST", body: { organizationId } });
}

/** Makes another Organization for the person and moves the session into it. */
export function createOrganization(name: string): Promise<void> {
  return requestJson<void>("/api/auth/organizations", { method: "POST", body: { name } });
}

/** Renames the Organization the session acts in. Only an admin may. */
export function renameOrganization(name: string): Promise<void> {
  return requestJson<void>("/api/organization", { method: "PATCH", body: { name } });
}
