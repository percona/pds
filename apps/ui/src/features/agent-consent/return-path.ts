/** The only path a sign in may send the browser back to. */
const authorizePrefix = "/oauth/authorize?";

/**
 * Where to go after signing in on behalf of an app, or null when the value is
 * not an authorization request of this control plane. The sign in page is
 * reachable by anybody and its `next` parameter by anybody who can send a
 * link, so anything but the one path the control plane itself sent is refused.
 * Nothing is accepted that a browser could read as another site: the prefix
 * fixes a single leading slash and the path.
 */
export function agentSignInReturn(next: string | null): string | null {
  if (next === null || !next.startsWith(authorizePrefix)) {
    return null;
  }
  // A control character or a backslash would let a value mean something other
  // than what it reads as once a browser or a proxy normalizes it.
  for (const character of next) {
    const code = character.charCodeAt(0);
    if (code <= 0x1f || code === 0x7f || character === "\\") {
      return null;
    }
  }

  return next;
}

/**
 * The address the app is sent back to, or null when it is not one a browser
 * should be sent to. The control plane only answers with the redirect address
 * the app proved was its own, so this is a second check on what the page is
 * about to navigate to, not the first.
 */
export function safeRedirect(url: string): string | null {
  try {
    const parsed = new URL(url);

    return parsed.protocol === "http:" || parsed.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}
