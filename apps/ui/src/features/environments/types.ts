export type EnvironmentStatus = "connected" | "unreachable";

/** What the list says about a support access window that is opening or open. */
export type SupportSummary = {
  state: "opening" | "open";
  level: "readOnly" | "admin";
  expiresAt: string;
};

export type Environment = {
  name: string;
  namespace?: string;
  status: EnvironmentStatus;
  lastSeen?: string;
  /** The Team the environment belongs to. Present when the list covers every Team. */
  team?: string;
  /** The support access window while one is opening or open. */
  supportAccess?: SupportSummary;
};

/**
 * What the environments list answers. The connect guide carries the addresses this
 * control plane is reachable at; this route is polled by every signed in
 * client, so it stays to what the environments page renders.
 */
export type EnvironmentSnapshot = {
  environments: Environment[];
};

export type ConnectGuide = {
  hubUrl?: string;
  gitUrl?: string;
  githubOrg?: string;
  namespace?: string;
  clusterRegion?: string;
};

export type JoinToken = {
  environment: string;
  token: string;
  command?: string;
  hubUrl?: string;
  region?: string;
  expiresAt?: string;
};

/**
 * How far monitoring is reachable. Cluster only is the default. Private and
 * public put it behind a cloud load balancer, internal or internet facing, the
 * same words a database uses.
 */
export type MonitoringAccessChoice = "cluster" | "private" | "public";

/** What the control plane lets an operator change about an environment's monitoring. */
export type MonitoringSettings = {
  access: MonitoringAccessChoice;
  /** The addresses the load balancer admits. Empty while access is cluster only. */
  sourceRanges?: string[];
  cpuMillicores: number;
  memoryMi: number;
  storageGi: number;
};

/**
 * Where an environment's monitoring login lives. It carries commands, never a
 * password: the control plane does not hold one for an environment it does not run.
 */
export type MonitoringAccess = {
  username: string;
  passwordCommand: string;
  portForwardCommand: string;
  /** Where the cloud says monitoring can be opened. Empty until it assigns one. */
  address: string;
  /** The address to sign in at: the environment's own when it has one. */
  url: string;
  /** The local end of the port-forward, for someone with access to the environment. */
  tunnelUrl: string;
};

export type Monitoring = {
  settings: MonitoringSettings;
  access: MonitoringAccess;
};
