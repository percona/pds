/** The one place the package name lives. The bridge snippet and the login command build from it. */
export const mcpPackage = "@gas-lab/pds-mcp@next";

/** The address the plugin's Cursor entry is fixed to. */
export const defaultOrigin = "https://pds.percona.com";

export type Access = "read" | "write" | "delete";

export const accessChoices: { id: Access; title: string; hint: string }[] = [
  { id: "read", title: "Read only", hint: "List databases, read metrics, alerts and logs." },
  { id: "write", title: "Read and change", hint: "Also create, scale and back up databases." },
  {
    id: "delete",
    title: "Read, change and delete",
    hint: "Also delete databases and restore in place.",
  },
];

export const claudeCodeInstall =
  "claude plugin marketplace add percona/pds\nclaude plugin install pds@percona-pds";

export const claudeCodeUpdate =
  "claude plugin marketplace update percona-pds\nclaude plugin update pds@percona-pds";

/** The plugin reads PDS_URL when Claude Code starts, and uses the default address without it. */
export function claudeCodeStart(origin: string): string {
  return `PDS_URL=${origin} claude`;
}

/** The control plane's own remote MCP address, for a client that signs in with OAuth itself. */
export function mcpAddress(origin: string): string {
  return `${origin}/mcp`;
}

const accessFlags: Record<Access, string> = { read: "read", write: "change", delete: "delete" };

/** The command a person runs once in a terminal to sign the bridge in as themselves. */
export function loginCommand(origin: string, access: Access): string {
  return `PDS_URL=${origin} npx -y ${mcpPackage} login --access ${accessFlags[access]}`;
}

/** The server entry for a client that can only run a local command. It holds no credential. */
export function bridgeConfig(origin: string): string {
  const entry = { command: "npx", args: ["-y", mcpPackage], env: { PDS_URL: origin } };

  return JSON.stringify({ mcpServers: { pds: entry } }, null, 2);
}
