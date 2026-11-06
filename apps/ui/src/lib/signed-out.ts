/**
 * A session can end while the tab is open: the customer signs out in another
 * tab, Percona resets their password, or the session simply runs out. The
 * request layer notices it first, as a 401 on a route that worked a minute
 * ago, and the gate around the app is what has to react.
 *
 * This is the wire between them. It lives in lib rather than in the auth
 * feature so the request layer can call it without importing a feature that
 * imports the request layer back.
 */

type Listener = () => void;

const listeners = new Set<Listener>();

/** Subscribes to the session ending. Returns the unsubscribe. */
export function onSignedOut(listener: Listener): () => void {
  listeners.add(listener);

  return () => {
    listeners.delete(listener);
  };
}

/** Reports that the adapter refused a request the session should have opened. */
export function notifySignedOut(): void {
  // A Set tolerates a listener unsubscribing itself mid iteration, which is
  // exactly what the gate does when it reacts, so this needs no copy.
  for (const listener of listeners) {
    listener();
  }
}
