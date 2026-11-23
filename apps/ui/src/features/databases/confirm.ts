/**
 * Whether a typed confirmation matches the database name. Delete asks for the
 * name typed out and reads it exact apart from surrounding whitespace, so a
 * name pasted out of the header is not refused for a trailing newline while a
 * different database still is.
 */
export function isNameConfirmed(typed: string, name: string): boolean {
  return typed.trim() === name;
}

/**
 * Said wherever a delete is announced. Deleting a PostgreSQL database leaves
 * its backup data in the backup storage, so the copy must not promise that the
 * backups go with it.
 */
export const POSTGRESQL_BACKUPS_STAY = "PostgreSQL backups stay in the backup storage.";

/**
 * Said wherever a delete is announced. Deleting a MongoDB database deletes its
 * backups with it, including the data they wrote to the backup storage.
 */
export const MONGODB_BACKUPS_DELETED = "MongoDB backups are deleted from the backup storage too.";
