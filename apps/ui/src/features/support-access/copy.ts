import type { SupportLevel } from "./types";

export const levelName: Record<SupportLevel, string> = { readOnly: "Read only", admin: "Admin" };

export const levelFacts: Record<SupportLevel, string> = {
  readOnly:
    "Can view the environment's Kubernetes resources, logs and events. Cannot change anything or read secrets.",
  admin:
    "Can create and change your databases, take and restore backups, and delete pods to restart them. Cannot delete databases or backups, read secrets, open a shell in a pod, create tokens or change access rules.",
};

export const recordNote =
  "PDS records when access opens, extends and ends, and who did it. It does not record what the engineer does. To see their requests, turn on audit logging on your Kubernetes cluster and look for their Percona login between the open and end times in this history.";

export const startFailed =
  "Support access could not start on this environment. Try again, or contact Percona support.";

export const supportHours = [1, 4, 24, 72] as const;

export const hoursLabel = (hours: number) => (hours === 1 ? "1 hour" : `${hours} hours`);

/** The most a window may run for, counted from now, however often it is extended. */
export const maxSupportHours = Math.max(...supportHours);

export const extendLabel = (hours: number) => `Until ${hoursLabel(hours)} from now`;
