import { formatBackupSize } from "./backups";
import { postgresqlNameLimit, postgresqlNameReason } from "./engines";
import type { Backup, DatabaseEngine } from "./types";

/**
 * Which backup a restore would read from, for the dialog opened off one row.
 * The size is left out when the operator has not reported one: the backup is
 * still restorable, and a zero would read as an empty one.
 */
export function restoreSource(backup: Backup): string {
  const kind = backup.scheduled ? "Scheduled backup" : "On-demand backup";
  const size = formatBackupSize(backup.sizeBytes);

  return size ? `${kind}, ${size}` : kind;
}

export type RestoreNameRule = {
  /** The longest name a restored copy may carry. */
  limit: number;
  /** Why the limit is shorter than the 63 characters every other name takes. */
  reason: string;
};

/**
 * The copy restores from a backup of the same engine, so the source's engine
 * decides the rule.
 *
 * For MySQL, the provider names its restore object `<copy>-datasource`, and the
 * operator names the restore job `restore-job-<restore>-<cluster>`, so the
 * label it builds is `2 * len(copy) + 24` characters. Kubernetes refuses a
 * label over 63, and the failure lands minutes later in an operator object
 * nobody is watching: the copy comes up as an empty database.
 *
 * A PostgreSQL copy is seeded as a new database, so its limit is the one every
 * PostgreSQL name has.
 */
const restoreNameRules: Record<DatabaseEngine, RestoreNameRule> = {
  mysql: {
    limit: 19,
    reason:
      "The provider builds its restore job name from this one twice over, and a longer one is refused.",
  },
  postgresql: { limit: postgresqlNameLimit, reason: postgresqlNameReason },
};

export function restoreNameRule(engine: DatabaseEngine): RestoreNameRule {
  return restoreNameRules[engine];
}

/**
 * DNS-1035 label, short enough for the engine's restore to build on. It starts
 * with a letter because the engine names a Service after the database.
 */
export function isRestoreDatabaseName(value: string, engine: DatabaseEngine): boolean {
  const trimmed = value.trim();

  return (
    trimmed.length <= restoreNameRule(engine).limit &&
    /^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$/.test(trimmed)
  );
}

/**
 * The name to offer for a restored copy. It is the source name with a suffix,
 * moved past whatever the environment already runs, so the field opens on a name
 * the adapter will accept rather than on one it has to refuse.
 */
export function restoreNameSuggestion(
  source: string,
  taken: string[],
  engine: DatabaseEngine,
): string {
  const used = new Set(taken.map((name) => name.trim()));
  const { limit } = restoreNameRule(engine);

  for (let attempt = 1; ; attempt += 1) {
    const suffix = `-restore-${attempt}`;
    const base = source
      .trim()
      .slice(0, limit - suffix.length)
      .replace(/-+$/, "");
    const candidate = `${base}${suffix}`;
    if (!used.has(candidate)) {
      return candidate;
    }
  }
}
