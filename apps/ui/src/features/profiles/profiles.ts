/** Profile identifiers, spelled the way the adapter reads them. */
export type ProfileId = "dev" | "staging" | "prod";

export type Profile = {
  id: ProfileId | "custom";
  label: string;
  /** The one line each card shows under its name. */
  consequence: string;
  members: number;
  cpuCores: number;
  memoryGb: number;
  storageGb: number;
};

/** The sizing table, in the order the picker shows it. */
export const profiles: readonly (Profile & { id: ProfileId })[] = [
  {
    id: "dev",
    label: "Dev",
    consequence: "1 member, on-demand backups",
    members: 1,
    cpuCores: 1,
    memoryGb: 2,
    storageGb: 20,
  },
  {
    id: "staging",
    label: "Staging",
    consequence: "3 members, HA, daily backups",
    members: 3,
    cpuCores: 2,
    memoryGb: 8,
    storageGb: 100,
  },
  {
    id: "prod",
    label: "Prod",
    consequence: "3 members, HA, daily backups",
    members: 3,
    cpuCores: 4,
    memoryGb: 16,
    storageGb: 250,
  },
];

export const defaultProfileId: ProfileId = "dev";

/**
 * Override bounds, mirrored from the engine plugin so the form can state the
 * range before a round trip. The adapter validates them again: the client is
 * never trusted.
 */
export const resourceBounds = {
  cpu: { min: 1, max: 64 },
  memory: { min: 1, max: 512 },
  storage: { min: 10, max: 16000 },
} as const;

/** Shown when a listed database has no catalog profile label. */
export const customProfile: Profile = {
  id: "custom",
  label: "Custom",
  consequence: "Sized on the Kubernetes cluster, not from a catalog profile",
  members: 0,
  cpuCores: 0,
  memoryGb: 0,
  storageGb: 0,
};

export function findProfile(id: ProfileId | string | undefined | null): Profile {
  const key = (id ?? "").trim().toLowerCase();
  const profile = profiles.find((candidate) => candidate.id === key);
  if (profile) {
    return profile;
  }

  return customProfile;
}
