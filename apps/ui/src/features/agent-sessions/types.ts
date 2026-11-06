/** How the control plane came to know the app: a metadata document, a registration, or the built in client. */
export type AgentKind = "cimd" | "dcr" | "local";

/**
 * One connected agent. The name and host are what the app declared about
 * itself. There is no token or client id here, and the console never has one.
 */
export type AgentSession = {
  id: string;
  name: string;
  /** Empty for the built in client. */
  host: string;
  kind: AgentKind;
  /** Who signed the agent in. */
  member: string;
  /** Whether the person looking is the one who signed it in. */
  mine: boolean;
  team: string;
  access: "read" | "change" | "change_delete";
  passwords: boolean;
  sqlWrites: boolean;
  createdAt: string;
  lastUsedAt: string;
  expiresAt: string;
};
