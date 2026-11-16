/** The product name, in full. Prose uses PDS after the first mention. */
export const productName = "Percona Database Service";

/**
 * Where "Sign in" goes: the console, which shares this host under /app/.
 * A build can point it somewhere else. Signup is the first sign in there.
 */
export const consoleUrl = import.meta.env.VITE_CONSOLE_URL || "/app/";

/** In-page anchors, shared by the header nav and the sections they point at. */
export const sectionIds = {
  tryIt: "try-it",
  operations: "operations",
  proof: "proof",
  pricing: "pricing",
  faq: "faq",
} as const;
