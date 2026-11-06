import { requestJson } from "@/lib/api";
import type {
  Backup,
  BackupScheduleInput,
  CreateDatabaseInput,
  Database,
  RestoreAccepted,
  RestoreDatabaseInput,
  ScaleDatabaseInput,
} from "./types";

/** `team` names the Team the database is created in, when it is not the one the console acts in. */
export function createDatabase(
  input: CreateDatabaseInput,
  team?: string,
  signal?: AbortSignal,
): Promise<Database> {
  return requestJson<Database>("/api/databases", { method: "POST", body: input, team, signal });
}

/**
 * Reads the list. The adapter answers `{databases: [...]}`, and a body without
 * that field means there is nothing to list rather than something to report.
 */
export async function fetchDatabases(
  environment?: string,
  signal?: AbortSignal,
): Promise<Database[]> {
  const query = environment ? `?environment=${encodeURIComponent(environment)}` : "";
  const payload = await requestJson<{ databases?: Database[] }>(`/api/databases${query}`, {
    signal,
  });

  return Array.isArray(payload?.databases) ? payload.databases : [];
}

export function databaseHref(id: string): string {
  return `/databases/${databaseResourcePath(id)}`;
}

/**
 * Path under `/api/databases` for one id. Ids are `environment/namespace.name` and
 * must stay two URL segments so chi can bind `{id}/{hubID}`. A segment of
 * `.` or `..`, or any other shape, is refused rather than encoded into a path.
 */
export function databaseResourcePath(id: string): string {
  const parts = id.split("/");
  if (parts.length === 2 && parts.every((part) => part !== "" && part !== "." && part !== "..")) {
    return parts.map((part) => encodeURIComponent(part)).join("/");
  }

  throw new Error("A database id needs an environment and a name.");
}

export function fetchDatabase(id: string, signal?: AbortSignal): Promise<Database> {
  return requestJson<Database>(`/api/databases/${databaseResourcePath(id)}`, { signal });
}

/**
 * Reads the real connection string, password included. This is the only call
 * in the app that moves a credential, and it runs on the copy click and nowhere
 * else. The value goes straight to the clipboard and is never held in state,
 * never rendered, and never logged.
 */
export async function fetchDatabaseCredential(id: string, signal?: AbortSignal): Promise<string> {
  const payload = await requestJson<{ uri?: string }>(
    `/api/databases/${databaseResourcePath(id)}/credential`,
    { signal },
  );

  return typeof payload?.uri === "string" ? payload.uri : "";
}

/**
 * Reads the backups of one database, newest first. As with the database list,
 * a body without the field means there is nothing to list.
 */
export async function fetchBackups(id: string, signal?: AbortSignal): Promise<Backup[]> {
  const payload = await requestJson<{ backups?: Backup[] }>(
    `/api/databases/${databaseResourcePath(id)}/backups`,
    { signal },
  );

  return Array.isArray(payload?.backups) ? payload.backups : [];
}

/**
 * Starts an on-demand backup. The adapter answers 202 with the backup it
 * created, which is pending until the operator picks it up.
 */
export function createBackup(id: string, signal?: AbortSignal): Promise<Backup> {
  return requestJson<Backup>(`/api/databases/${databaseResourcePath(id)}/backup`, {
    method: "POST",
    signal,
  });
}

/**
 * Writes the database's one backup schedule. Without a body the adapter turns
 * on the default daily schedule, which is what a chat schedule action sends,
 * and repeating that is harmless. The adapter answers with the database,
 * carrying the schedule it just wrote.
 */
export function setBackupSchedule(
  id: string,
  schedule?: BackupScheduleInput,
  signal?: AbortSignal,
): Promise<Database> {
  return requestJson<Database>(`/api/databases/${databaseResourcePath(id)}/backup-schedule`, {
    method: "POST",
    body: schedule,
    signal,
  });
}

/**
 * Restores a database from one of its backups. The typed confirmation happens
 * in the dialog; the adapter answers 202 and refuses a backup it cannot restore
 * from.
 */
export function restoreDatabase(
  id: string,
  input: RestoreDatabaseInput,
  signal?: AbortSignal,
): Promise<RestoreAccepted> {
  return requestJson<RestoreAccepted>(`/api/databases/${databaseResourcePath(id)}/restore`, {
    method: "POST",
    body: input,
    signal,
  });
}

/**
 * Deletes a database. The typed confirmation happens in the dialog; the
 * adapter answers 204 with no body, and the cascade deletion policy set at
 * create time takes the backups and restores with the instance.
 */
export function deleteDatabase(id: string, signal?: AbortSignal): Promise<void> {
  return requestJson<void>(`/api/databases/${databaseResourcePath(id)}`, {
    method: "DELETE",
    signal,
  });
}

/** Scales a database. The answer is the database as it now reads. */
export function scaleDatabase(
  id: string,
  input: ScaleDatabaseInput,
  signal?: AbortSignal,
): Promise<Database> {
  return requestJson<Database>(`/api/databases/${databaseResourcePath(id)}/scale`, {
    method: "POST",
    body: input,
    signal,
  });
}
