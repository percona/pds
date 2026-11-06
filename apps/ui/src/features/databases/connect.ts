import type { Database } from "./types";

/**
 * Stands in for the password on screen. Fixed width, so it leaks no length,
 * and the same glyph the adapter masks a connection string with.
 */
export const passwordMask = "••••••••";

/**
 * The connection string as the page shows it. The password is the mask.
 *
 * A hub often has the host, port and user without a masked string of its own,
 * because the real string arrives only when someone copies it. The shape is
 * the same either way.
 */
export function maskedConnectionUri(database: Database): string {
  const connection = database.connection;
  if (!connection?.host) {
    return connection?.maskedUri ?? "";
  }
  if (connection.maskedUri?.includes(connection.host)) {
    return connection.maskedUri;
  }

  const scheme = database.engine === "postgresql" ? "postgres" : "mysql";
  const host =
    connection.host.includes(":") && !connection.host.startsWith("[")
      ? `[${connection.host}]`
      : connection.host;
  const user = connection.username ? `${connection.username}:${passwordMask}@` : "";

  // The provider's PostgreSQL string names the database and requires TLS,
  // which PgBouncer enforces and some drivers do not try unless asked.
  const path = database.engine === "postgresql" ? "/postgres?sslmode=require" : "";

  return `${scheme}://${user}${host}:${connection.port}${path}`;
}

/** The address a port-forward answers on, once one is running. */
export const forwardedHost = "127.0.0.1";

/**
 * The client command for this engine.
 *
 * An empty password means there is none to hand over, which is the hub, or a
 * string this cannot read one out of. Either way the command falls back to the
 * form that asks for one, because a prompt still connects and an empty
 * password does not.
 *
 * The password is interpolated the same way for the mask and for the real
 * value, so the shape on screen is the shape on the clipboard.
 */
export function cliCommand(database: Database, password: string): string {
  const { connection } = database;
  if (!connection) {
    return "";
  }

  const { host, port, username } = connection;

  if (database.engine === "postgresql") {
    const psql = `psql -h ${quote(host)} -p ${quote(String(port))} -U ${quote(username)}`;

    return password ? `PGPASSWORD=${quote(password)} ${psql}` : psql;
  }

  const mysql = `mysql -h ${quote(host)} -P ${quote(String(port))} -u ${quote(username)}`;

  return password ? `${mysql} -p${quote(password)}` : `${mysql} -p`;
}

/**
 * The port-forward that puts an in-cluster database on a local port.
 *
 * Empty when the adapter reported no Service, which means the host is not an
 * in-cluster address and there is nothing to forward.
 */
export function portForwardCommand(database: Database): string {
  const { connection } = database;
  if (!connection) {
    return "";
  }
  const service =
    connection.serviceName || serviceNameFromHost(connection.host, database.namespace);
  if (!service) {
    return "";
  }

  return `kubectl -n ${quote(database.namespace)} port-forward ${quote(`svc/${service}`)} ${quote(`${connection.port}:${connection.port}`)}`;
}

/**
 * The client command against a running port-forward, which is the form that
 * works from a laptop. The password is asked for rather than passed: on a hub
 * there is none to pass, and step two already printed it.
 */
export function forwardedCliCommand(database: Database): string {
  const { connection } = database;
  if (!connection) {
    return "";
  }

  const { port, username } = connection;
  if (database.engine === "postgresql") {
    return `psql -h ${forwardedHost} -p ${quote(String(port))} -U ${quote(username)}`;
  }

  return `mysql -h ${forwardedHost} -P ${quote(String(port))} -u ${quote(username)} -p`;
}

/**
 * The same connection string against a running port-forward.
 *
 * Only the host is replaced, so a path, a query and a password holding an at
 * sign all survive. Empty when the string cannot be split safely, which the
 * caller reports rather than pasting a mangled address.
 */
export function forwardedUri(uri: string, port: number): string {
  const parts = splitUri(uri);
  if (!parts) {
    return "";
  }

  const userInfo = parts.userInfo ? `${parts.userInfo}@` : "";

  return `${parts.prefix}${userInfo}${forwardedHost}:${port}${parts.tail}`;
}

/**
 * Reads the connection Secret on the environment the database runs on. This is the
 * path to a credential the adapter cannot hand over itself.
 *
 * Every field is printed decoded rather than one named key, because the key a
 * provider writes the URI under is its own choice and a wrong key prints
 * nothing at all.
 */
export function readSecretCommand(database: Database): string {
  const namespace = quote(database.namespace);
  // One password, on its own, is what gets pasted at a client prompt. Printing
  // every field puts five look-alike lines next to the one that is wanted, and
  // the wrong one reads as a rejected password rather than as a mis-copy. The
  // fallback stays for a Secret that carries no password key, where naming
  // every field is the only honest answer.
  const template =
    `{{if .data.password}}{{.data.password|base64decode}}{{"\\n"}}` +
    `{{else}}{{range $k,$v := .data}}{{$k}}={{$v|base64decode}}{{"\\n"}}{{end}}{{end}}`;
  const secretName = database.connection?.secretName;
  if (secretName) {
    return `kubectl -n ${namespace} get secret ${quote(secretName)} -o go-template=${quote(template)}`;
  }

  // A spoke reports host, port and user on its heartbeat but not which Secret
  // holds the password, and the name is the operator's to choose. The
  // instance names it, so ask the environment rather than guess.
  const name = quote(database.name);

  return (
    `kubectl -n ${namespace} get secret ` +
    `"$(kubectl -n ${namespace} get instances ${name} ` +
    `-o jsonpath='{.status.connectionSecretRef.name}')" ` +
    `-o go-template=${quote(template)}`
  );
}

/**
 * Points kubectl at the environment a database runs on.
 *
 * The hub never touches a spoke's API, so every command it prints runs
 * against that environment's own context. The name is the environment as the hub
 * lists it, which is the name the join used.
 */
export function useContextCommand(environment: string): string {
  return environment ? `kubectl config use-context ${quote(environment)}` : "";
}

/**
 * The Service in front of this database, from the in-cluster host.
 *
 * A spoke that reported no Service name still reported a host, and an
 * in-cluster host is `<service>.<namespace>`, so the port-forward is known
 * either way.
 */
export function serviceNameFromHost(host: string, namespace: string): string {
  if (!host || !namespace) {
    return "";
  }

  const labels = host.split(".");

  return labels.length >= 2 && labels[1] === namespace ? (labels[0] ?? "") : "";
}

/**
 * Why the connect dialog has nothing to offer yet, or empty when it does.
 *
 * The connect card renders this in place of the connection, and the header
 * disables its Connect button on the same answer. It has to say what happens
 * next rather than restate that something is missing.
 */
export function connectBlockedReason(database: Database): string {
  if (database.connection) {
    return "";
  }

  if (database.status === "deleting") {
    return `${database.name} is being deleted.`;
  }

  if (database.status === "failed") {
    return `${database.name} did not come up, so it published no connection.`;
  }

  return `${database.name} is still coming up. Connection details appear once it is ready.`;
}

/**
 * Reads the password out of a connection string.
 *
 * The credential route answers with a URI, and a client command wants the
 * password on its own. Empty when the string carries none, which leaves the
 * command in its prompting form rather than building one around an empty
 * password that fails to authenticate.
 */
export function passwordFromUri(uri: string): string {
  const parts = splitUri(uri);
  if (!parts) {
    return "";
  }

  const separator = parts.userInfo.indexOf(":");
  if (separator === -1) {
    return "";
  }

  const password = parts.userInfo.slice(separator + 1);

  // The adapter percent-encodes when it builds a URI from parts, but a
  // provider that published its own string may not have, and a bare percent
  // sign makes decoding throw. The raw value is the better answer than an
  // error that reaches the developer as a failed copy.
  try {
    return decodeURIComponent(password);
  } catch {
    return password;
  }
}

type UriParts = {
  /** Everything up to and including the scheme separator. */
  prefix: string;
  /** User and password, without the trailing at sign. Empty when there is none. */
  userInfo: string;
  tail: string;
};

/**
 * Splits a connection string into the pieces the commands are built from, or
 * null when it cannot be split safely.
 *
 * A password holding an unescaped slash, question mark or hash ends the
 * authority early and takes the at sign with it. Guessing there welds half a
 * password onto the path, so this refuses instead. The adapter refuses the
 * same shape when it masks a URI, for the same reason.
 */
function splitUri(uri: string): UriParts | null {
  const schemeEnd = uri.indexOf("://");
  if (schemeEnd === -1) {
    return null;
  }

  const authorityStart = schemeEnd + "://".length;
  const rest = uri.slice(authorityStart);
  const cut = rest.search(/[/?#]/);
  const authority = cut === -1 ? rest : rest.slice(0, cut);
  const tail = cut === -1 ? "" : rest.slice(cut);
  const prefix = uri.slice(0, authorityStart);

  // A password may contain an at sign, so the last one ends the user info.
  const userInfoEnd = authority.lastIndexOf("@");
  if (userInfoEnd === -1) {
    return rest.includes("@") ? null : { prefix, userInfo: "", tail };
  }

  return { prefix, userInfo: authority.slice(0, userInfoEnd), tail };
}

/**
 * Wraps a value for a shell. A generated password can hold a quote, and an
 * unescaped one ends the string early and pastes a command that does not run.
 */
function quote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}
