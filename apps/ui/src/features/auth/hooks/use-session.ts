import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type Query,
  type QueryKey,
} from "@tanstack/react-query";
import { fetchSession, signIn, signOut } from "../api";
import type { Session, SignInInput } from "../types";

/** The one query key the session lives under, so a sign out can clear it. */
export const sessionKey = ["auth", "session"] as const;

/** The flag set is read once per sign in, so it has a key a switch can keep. */
export const flagsKey = ["auth", "features"] as const;

/**
 * Drops cached product state for whoever this browser was signed in as.
 *
 * The session is set to null rather than removed. A reader still mounted
 * would refetch a removed query, and the answer is already known.
 */
export function forgetSignedInState(queryClient: QueryClient): void {
  const sessionHash = JSON.stringify(sessionKey);
  queryClient.removeQueries({ predicate: (query) => query.queryHash !== sessionHash });
  queryClient.setQueryData(sessionKey, null);
}

/**
 * Matches what was cached for an Organization or Team, which is everything but
 * the session and the flag set, and anything in `alsoKeep`.
 */
export function scopedQueries(...alsoKeep: QueryKey[]): (query: Query) => boolean {
  const keep = new Set([sessionKey, flagsKey, ...alsoKeep].map((key) => JSON.stringify(key)));

  return (query) => !keep.has(query.queryHash);
}

/** Drops what was cached for the Organization the console just left. */
export function dropScopedQueries(queryClient: QueryClient): void {
  queryClient.removeQueries({ predicate: scopedQueries() });
}

/**
 * Reads the session. Signed out is data, not an error: undefined while the
 * first read is in flight, null once the adapter says nobody is signed in.
 *
 * It never retries. A 401 is a settled answer, and retrying it would only
 * delay the login page.
 */
export function useSession() {
  return useQuery({
    queryKey: sessionKey,
    queryFn: fetchSession,
    retry: false,
    staleTime: Infinity,
  });
}

/**
 * Whether the signed in person is an admin of the Organization they act in.
 * It only decides what the console shows: the control plane refuses an admin
 * route to anybody else whatever the console does.
 */
export function useIsAdmin(): boolean {
  return useSession().data?.role === "admin";
}

/** Signs in and seeds the session, so the gate opens without a second read. */
export function useSignIn() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: SignInInput) => signIn(input),
    onSuccess: (session: Session) => {
      queryClient.setQueryData(sessionKey, session);
    },
  });
}

/**
 * Signs out. Everything cached goes, because it belongs to the customer who is
 * leaving.
 *
 * The session is set to null rather than removed. A reader still mounted would
 * refetch a removed query, and the answer is already known: clearing it and
 * then asking again would race the gate that is about to navigate away.
 *
 * It clears on success only. A logout the adapter refused leaves the session
 * row and the cookie live, and showing the login page over a session that
 * still works would be the button lying about what it did.
 */
export function useSignOut() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: signOut,
    onSuccess: () => {
      forgetSignedInState(queryClient);
    },
  });
}
