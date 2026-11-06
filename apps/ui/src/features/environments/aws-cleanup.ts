import { guidePath } from "./guide-path";

/** Where the AWS cleanup guide for an environment lives. */
export function awsCleanupPath(environment: string, namespace?: string): string {
  return guidePath(environment, "aws-cleanup", namespace);
}

/** The namespace a join puts databases in unless it was told another. */
export const joinDatabaseNamespace = "pds";

/** The tag a join puts on everything it creates, with the environment name as its value. */
export const joinOwnershipTag = "percona.com/pds";
