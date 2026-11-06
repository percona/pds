/**
 * Where the console is mounted. The root of the host serves the public landing
 * page, so the console lives under /app/ and every URL it builds starts there.
 * It comes from `base` in vite.config.ts.
 */
export const basePath = import.meta.env.BASE_URL;

/** The same mount without its trailing slash, as React Router wants it. */
export const routerBasename = basePath.replace(/\/$/, "");

/** A file from public/, which is served under the mount like everything else. */
export function assetUrl(name: string): string {
  return `${basePath}${name}`;
}
