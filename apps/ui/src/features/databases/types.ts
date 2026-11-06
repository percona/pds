import type { ProfileId } from "@/features/profiles";

export type DatabaseEngine = "mysql" | "postgresql";

/**
 * Normalized lifecycle phases the adapter reports. Every engine maps its own
 * vocabulary onto these, so the UI has one set of states to render.
 */
export type DatabasePhase =
  | "pending"
  | "provisioning"
  | "initializing"
  | "ready"
  | "updating"
  | "deleting"
  | "failed"
  | "restoring"
  | "suspending"
  | "suspended"
  | "resuming"
  | "unknown";

/**
 * How an application reaches a database. Nothing here is a secret: the real
 * connection string is fetched on click from the credential route, so a page
 * view never moves a password and a list never moves one for every database.
 */
export type DatabaseConnection = {
  host: string;
  port: number;
  username: string;
  /**
   * The connection string with the password replaced, for display only. The
   * adapter omits it when there is nothing to mask, and a hub never has one,
   * so this is absent more often than not.
   */
  maskedUri?: string;
  /** The Secret the credentials live in, when the adapter reports one. */
  secretName?: string;
  /**
   * The Service a port-forward targets. The adapter omits it when the host is
   * not an in-cluster address, and the connect dialog then offers no
   * port-forward rather than a command that cannot work.
   */
  serviceName?: string;
};

/**
 * Where a database accepts connections from. Cluster has no load balancer,
 * which only databases created before every create got one still have.
 * Private is an internal load balancer on a VPC address, public an
 * internet-facing one.
 */
export type DatabaseVisibility = "cluster" | "private" | "public";

/** The load balancer a create asks for. */
export type DatabaseAccessChoice = "private" | "public";

/**
 * Where a database can be reached from. An external database sits behind a
 * load balancer in the customer's cloud account, and its address arrives some
 * time after the database is created.
 */
export type DatabaseAccess = {
  /** Any load balancer, private or public. */
  external: boolean;
  /** Absent from an adapter older than private access, where external meant public. */
  visibility?: DatabaseVisibility;
  /** Absent on a private database, and on an older public one with no allow list. */
  sourceRanges?: string[];
  /** Absent until the load balancer has an address. */
  address?: string;
};

export type BackupSchedulePeriod = "hourly" | "daily" | "weekly";

/**
 * The recurring backup an instance spec carries. The adapter reads it back off
 * the spec, so it states what the environment will do rather than what the profile
 * asked for. Absent means on-demand backups only. Every time is UTC.
 */
export type BackupSchedule = {
  name: string;
  /**
   * False for a schedule turned off, which keeps its settings. Absent from an
   * adapter older than the editor, which only reported running schedules.
   */
  enabled?: boolean;
  /** Standard cron syntax, in UTC. */
  cron: string;
  /** How many copies the schedule keeps. Absent when it keeps all of them. */
  retentionCopies?: number;
  /** Absent for a cron the control plane did not write. */
  period?: BackupSchedulePeriod;
  minute?: number;
  /** Absent on an hourly schedule. */
  hour?: number;
  /** 0 is Sunday. Only on a weekly schedule. */
  weekday?: number;
};

/**
 * The `POST /api/databases/:id/backup-schedule` body, in UTC. Turning the
 * schedule off sends `enabled: false` alone and the adapter keeps the rest.
 */
export type BackupScheduleInput =
  | { enabled: false }
  | {
      enabled: true;
      period: BackupSchedulePeriod;
      minute: number;
      hour?: number;
      weekday?: number;
      retentionCopies: number;
    };

/** Normalized backup states the adapter reports. */
export type BackupPhase = "pending" | "running" | "succeeded" | "failed" | "deleting" | "unknown";

/** One backup as `GET /api/databases/:id/backups` returns it. */
export type Backup = {
  id: string;
  status: BackupPhase;
  /** Zero until the operator reports a size, which is after it finishes. */
  sizeBytes: number;
  /** True when the schedule produced it, false for an on-demand backup. */
  scheduled: boolean;
  createdAt?: string;
  finishedAt?: string;
  /** Operator reason, usually on a failed backup. */
  message?: string;
};

/** One database as `GET /api/databases/:id` returns it. */
export type Database = {
  id: string;
  name: string;
  engine: DatabaseEngine;
  /** The engine version, when the adapter reports one. */
  version?: string;
  /** Catalog name, or custom when the environment did not record one. */
  profile: ProfileId | "custom" | "";
  namespace: string;
  status: DatabasePhase;
  /** The last operator message, when the adapter has one. */
  message?: string;
  members: number;
  cpu: number;
  memoryGb: number;
  storageGb: number;
  createdAt?: string;
  /** When the last backup finished. Absent while a database has none. */
  lastBackupAt?: string;
  /** Absent when this database takes on-demand backups only. */
  backupSchedule?: BackupSchedule;
  /** Absent until the provider publishes the connection Secret. */
  connection?: DatabaseConnection;
  /** Absent from an adapter older than external access, which is internal. */
  access?: DatabaseAccess;
  /** The environment this database runs on. */
  environment: string;
  /** The Team the database belongs to. Present when the list covers every Team. */
  team?: string;
};

/** The `POST /api/databases/:id/restore` payload. */
export type RestoreDatabaseInput = {
  backupId: string;
  /** The name of the database the restore creates. */
  name: string;
};

/**
 * The `POST /api/databases/:id/restore` answer. A restore takes minutes, so the
 * adapter accepts the request and the database phase carries it from there.
 * The answer names the database it created, which is not the one the request
 * was addressed to.
 */
export type RestoreAccepted = {
  status: string;
  environment?: string;
  name?: string;
  id?: string;
};

/**
 * The `POST /api/databases` payload. Name, engine, profile, and environment are
 * required. The form defaults engine to mysql.
 *
 * There is no namespace: the adapter creates in the one namespace it manages
 * and rejects any other value.
 */
export type CreateDatabaseInput = {
  name: string;
  engine: DatabaseEngine;
  profile: ProfileId;
  environment: string;
  cpu?: number;
  memory?: number;
  storage?: number;
  /** Absent lets the adapter choose, which is private. */
  access?: DatabaseAccessChoice;
  /** Required when access is public. Open to the internet is 0.0.0.0/0. */
  sourceRanges?: string[];
};

/**
 * The `POST /api/databases/:id/scale` payload. Every field is optional on its
 * own and only the ones that changed are sent; the adapter refuses a request
 * that changes nothing. Replicas is the member count: 1 to 3 enables HA.
 */
export type ScaleDatabaseInput = {
  cpu?: number;
  memory?: number;
  storage?: number;
  replicas?: number;
};
