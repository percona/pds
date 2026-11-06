import { useCallback, useState } from "react";

const storagePrefix = "pds-connection-copied:";

/**
 * Whether the developer has copied this database's connection string before.
 * The overview leads with the connection card until they have, then gives the
 * top spot to metrics. It is remembered per database and survives a reload,
 * because the answer to "have I connected yet" does not change on one.
 */
export function useConnectionCopied(id: string) {
  const [state, setState] = useState(() => ({ id, copied: read(id) }));

  // Moving between two databases keeps this component mounted, so the answer is
  // re-read rather than carried over from the one before.
  if (state.id !== id) {
    setState({ id, copied: read(id) });
  }

  const markCopied = useCallback(() => {
    setState({ id, copied: true });
    write(id);
  }, [id]);

  return { copied: state.copied, markCopied };
}

// Storage can be blocked or full. Neither is worth breaking the page over: the
// card just leads again on the next visit.
function read(id: string): boolean {
  try {
    return window.localStorage.getItem(storagePrefix + id) === "true";
  } catch {
    return false;
  }
}

function write(id: string): void {
  try {
    window.localStorage.setItem(storagePrefix + id, "true");
  } catch {
    // Ignored on purpose.
  }
}
