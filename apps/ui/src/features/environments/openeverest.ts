import { guidePath } from "./guide-path";

/** Where the guide for keeping an environment's databases and running them with OpenEverest lives. */
export function openEverestPath(environment: string, namespace?: string): string {
  return guidePath(environment, "openeverest", namespace);
}
