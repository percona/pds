/** DNS-1123 label: lowercase, digits, dashes, 1 to 63 characters. */
export function isEnvironmentName(value: string): boolean {
  return /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(value);
}

/**
 * The one command a customer pastes. With `update` it moves an environment that
 * already joined, and the script takes the token and nothing else beside it.
 */
export function joinEnvironmentCommand(opts: {
  hubUrl: string;
  token: string;
  update?: boolean;
}): string {
  const hubUrl = opts.hubUrl.replace(/\/$/, "");
  const mode = opts.update ? "--update " : "";

  return `curl -fsSL ${quote(`${hubUrl}/api/internal/connect/bootstrap.sh`)} | bash -s -- ${mode}--token ${quote(opts.token)}`;
}

/**
 * The command that deletes what an environment's join created in AWS. It finds
 * everything by tag and never calls the control plane, so it carries no token.
 * The region is read from the reader's own shell, because a delete in the
 * wrong one finds nothing. The name lands in the command unquoted, so the
 * caller must have checked it with isEnvironmentName.
 */
export function deleteEnvironmentCommand(opts: { hubUrl: string; name: string }): string {
  const hubUrl = opts.hubUrl.replace(/\/$/, "");

  return `curl -fsSL ${quote(`${hubUrl}/api/internal/connect/bootstrap.sh`)} | bash -s -- --delete --name ${opts.name} --region "$REGION"`;
}

function quote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

/**
 * Names the router would swallow. /environments/new is the create page, and
 * /environments/connect would be an environment named connect, so either name
 * has a row that cannot open its databases.
 */
const reserved = new Set(["connect", "new"]);

export function reservedEnvironmentName(value: string): boolean {
  return reserved.has(value.trim());
}
