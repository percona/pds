/** The most agents and the Chat may do for an organization. */
export type AiAccess = "read" | "change" | "change_delete";

export type AiSettings = {
  access: AiAccess;
  /** Whether local agents may fetch database passwords. */
  passwords: boolean;
  /** Whether local agents may change data with SQL. */
  sqlWrites: boolean;
};
