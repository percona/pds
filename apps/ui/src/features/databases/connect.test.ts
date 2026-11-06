import { describe, expect, it } from "vitest";
import { createMockDatabase } from "@/test/utils";
import {
  cliCommand,
  connectBlockedReason,
  forwardedCliCommand,
  forwardedUri,
  maskedConnectionUri,
  passwordFromUri,
  passwordMask,
  portForwardCommand,
  readSecretCommand,
  useContextCommand,
} from "./connect";

const mysql = createMockDatabase({
  name: "orders-dev",
  namespace: "pds",
  connection: {
    host: "orders-dev-haproxy.pds",
    port: 3306,
    username: "app",
    maskedUri: `mysql://app:${passwordMask}@orders-dev-haproxy.pds:3306`,
    secretName: "orders-dev-conn",
    serviceName: "orders-dev-haproxy",
  },
});

const postgres = createMockDatabase({
  name: "orders-dev",
  engine: "postgresql",
  namespace: "pds",
  connection: {
    host: "orders-dev-pgbouncer.pds",
    port: 5432,
    username: "app",
    secretName: "orders-dev-conn",
    serviceName: "orders-dev-pgbouncer",
  },
});

describe("cliCommand", () => {
  it("builds a mysql command with the password inline", () => {
    expect(cliCommand(mysql, "s3cr3t")).toBe(
      "mysql -h 'orders-dev-haproxy.pds' -P '3306' -u 'app' -p's3cr3t'",
    );
  });

  it("builds a psql command with the password in the environment", () => {
    expect(cliCommand(postgres, "s3cr3t")).toBe(
      "PGPASSWORD='s3cr3t' psql -h 'orders-dev-pgbouncer.pds' -p '5432' -U 'app'",
    );
  });

  // A generated password can hold a quote, and an unescaped one would end the
  // shell string early and paste a command that does not run.
  it("escapes a single quote in the password", () => {
    expect(cliCommand(mysql, "it's")).toContain(`-p'it'\\''s'`);
  });

  // A hub holds no password, and neither does a string this cannot read one
  // out of. The prompting form is the one shape that still runs, so it is what
  // the screen shows and what the clipboard gets. Building -p'' instead pastes
  // a command that authenticates as nobody and fails at the server.
  it("falls back to the prompting form when there is no password", () => {
    expect(cliCommand(mysql, "")).toBe("mysql -h 'orders-dev-haproxy.pds' -P '3306' -u 'app' -p");
    expect(cliCommand(postgres, "")).toBe("psql -h 'orders-dev-pgbouncer.pds' -p '5432' -U 'app'");
  });

  it("returns an empty string when the database has no connection", () => {
    expect(cliCommand(createMockDatabase({ connection: undefined }), "s3cr3t")).toBe("");
  });

  it("quotes a host and username that carry shell syntax", () => {
    const injected = createMockDatabase({
      connection: {
        host: "x; curl evil",
        port: 3306,
        username: "app;id",
        secretName: "orders-dev-conn",
        serviceName: "orders-dev-haproxy",
      },
    });

    expect(cliCommand(injected, "s3cr3t")).toBe(
      "mysql -h 'x; curl evil' -P '3306' -u 'app;id' -p's3cr3t'",
    );
  });
});

describe("portForwardCommand", () => {
  it("targets the Service in the database namespace", () => {
    expect(portForwardCommand(mysql)).toBe(
      "kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'",
    );
  });

  // No Service name means the host is not an in-cluster address, so there is
  // nothing to forward and the tab does not render.
  // A spoke reports no Service name, and an in-cluster host is
  // <service>.<namespace>, so the forward is known without it.
  it("derives the Service from an in-cluster host", () => {
    const sparse = createMockDatabase({
      namespace: "pds",
      connection: { host: "orders-dev-haproxy.pds", port: 3306, username: "app" },
    });

    expect(portForwardCommand(sparse)).toBe(
      "kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'",
    );
  });

  it("gives nothing when the adapter reported no Service", () => {
    const external = createMockDatabase({
      connection: { host: "db.example.com", port: 3306, username: "app" },
    });

    expect(portForwardCommand(external)).toBe("");
  });

  it("quotes a namespace and Service name that carry shell syntax", () => {
    const injected = createMockDatabase({
      namespace: "ns;id",
      connection: {
        host: "orders-dev-haproxy.pds",
        port: 3306,
        username: "app",
        serviceName: "svc; curl evil",
      },
    });

    expect(portForwardCommand(injected)).toBe(
      "kubectl -n 'ns;id' port-forward 'svc/svc; curl evil' '3306:3306'",
    );
  });
});

describe("useContextCommand", () => {
  it("points kubectl at the environment the hub lists", () => {
    expect(useContextCommand("spoke-a")).toBe("kubectl config use-context 'spoke-a'");
  });

  it("gives nothing off a hub, where kubectl is already pointed", () => {
    expect(useContextCommand("")).toBe("");
  });
});

describe("forwardedCliCommand", () => {
  // The password came from the Secret in the step before, so this asks for it
  // rather than carrying one the hub does not hold.
  it("connects to the forward and asks for the password", () => {
    expect(forwardedCliCommand(mysql)).toBe("mysql -h 127.0.0.1 -P '3306' -u 'app' -p");
    expect(forwardedCliCommand(postgres)).toBe("psql -h 127.0.0.1 -p '5432' -U 'app'");
  });

  it("gives nothing when the database has no connection", () => {
    expect(forwardedCliCommand(createMockDatabase({ connection: undefined }))).toBe("");
  });
});

describe("forwardedUri", () => {
  it("swaps the host for the forwarded address and keeps the rest", () => {
    expect(forwardedUri("mysql://app:s3cr3t@orders-dev-haproxy.pds:3306", 3306)).toBe(
      "mysql://app:s3cr3t@127.0.0.1:3306",
    );
  });

  it("keeps a path and a query", () => {
    expect(forwardedUri("postgres://app:s3cr3t@host.ns:5432/app?sslmode=require", 5432)).toBe(
      "postgres://app:s3cr3t@127.0.0.1:5432/app?sslmode=require",
    );
  });

  // A password may hold an at sign, so the last one ends the user info.
  it("reads the last at sign as the end of the user info", () => {
    expect(forwardedUri("mysql://app:p@ss@host.ns:3306", 3306)).toBe(
      "mysql://app:p@ss@127.0.0.1:3306",
    );
  });

  it("gives nothing for a string it cannot parse", () => {
    expect(forwardedUri("not-a-uri", 3306)).toBe("");
  });

  // A password holding an unescaped slash ends the authority early. Guessing
  // here welded the rest of the password onto the path and handed over an
  // address that points at nothing.
  it("refuses a string whose user info cannot be found", () => {
    expect(forwardedUri("mysql://app:a/b@host.ns:3306", 3306)).toBe("");
  });
});

describe("readSecretCommand", () => {
  // The password is pasted at a client prompt, so it is printed on its own.
  // Every field next to it reads as five ways to get the password wrong.
  it("prints the password alone, from the Secret in the right namespace", () => {
    const command = readSecretCommand(mysql);

    expect(command).toContain("kubectl -n 'pds' get secret 'orders-dev-conn'");
    expect(command).toContain("{{if .data.password}}{{.data.password|base64decode}}");
  });

  // A Secret that carries no password key still has to say something, and
  // naming every field is better than printing nothing.
  it("falls back to every field when the Secret has no password key", () => {
    expect(readSecretCommand(mysql)).toContain(
      "{{else}}{{range $k,$v := .data}}{{$k}}={{$v|base64decode}}",
    );
  });

  // A spoke reports no Secret name on its heartbeat, and the name is the
  // operator's to choose, so the instance is asked for it rather than guessed.
  it("resolves the Secret through the instance when none was reported", () => {
    const unpublished = createMockDatabase({
      name: "orders-dev",
      connection: { host: "orders-dev-haproxy.pds", port: 3306, username: "app" },
    });

    const command = readSecretCommand(unpublished);
    expect(command).toContain(
      "kubectl -n 'pds' get instances 'orders-dev' " +
        "-o jsonpath='{.status.connectionSecretRef.name}'",
    );
    expect(command).toContain("kubectl -n 'pds' get secret ");
  });

  it("quotes a Secret name that carries shell syntax", () => {
    const injected = createMockDatabase({
      connection: {
        host: "orders-dev-haproxy.pds",
        port: 3306,
        username: "app",
        secretName: "conn; curl evil",
      },
    });

    expect(readSecretCommand(injected)).toContain("get secret 'conn; curl evil'");
    expect(readSecretCommand(injected)).not.toContain("get secret conn;");
  });
});

describe("passwordFromUri", () => {
  it("reads the password out of a connection string", () => {
    expect(passwordFromUri("mysql://app:s3cr3t@host.ns:3306")).toBe("s3cr3t");
  });

  it("reads a password holding an at sign", () => {
    expect(passwordFromUri("mysql://app:p@ss@host.ns:3306")).toBe("p@ss");
  });

  it("decodes a percent-encoded password", () => {
    expect(passwordFromUri("postgres://app:a%2Fb@host.ns:5432/app")).toBe("a/b");
  });

  it("decodes the characters a URL has to hide", () => {
    expect(passwordFromUri("mysql://root:a%3Eb%23c%7Dd%2Ae%3Ff%21@host:3306/?tls=false")).toBe(
      "a>b#c}d*e?f!",
    );
  });

  // A provider that published its own string may not have encoded it, and a
  // bare percent sign makes decoding throw. The raw value beats an exception
  // that reaches the developer as a failed copy.
  it("keeps a password that is not valid percent encoding", () => {
    expect(passwordFromUri("mysql://app:100%@host.ns:3306")).toBe("100%");
  });

  it("gives nothing when the user info cannot be found", () => {
    expect(passwordFromUri("mysql://app:a/b@host.ns:3306")).toBe("");
  });

  // Without a password the command stays in its prompting form, which still
  // runs, rather than authenticating with an empty string, which does not.
  it("gives nothing when the string carries no password", () => {
    expect(passwordFromUri("mysql://app@host.ns:3306")).toBe("");
    expect(passwordFromUri("not-a-uri")).toBe("");
    expect(passwordFromUri("")).toBe("");
  });
});

describe("connectBlockedReason", () => {
  it("gives nothing when there is a connection to offer", () => {
    expect(connectBlockedReason(mysql)).toBe("");
  });

  it("says a database still coming up will have one", () => {
    expect(
      connectBlockedReason(
        createMockDatabase({ name: "orders-dev", status: "provisioning", connection: undefined }),
      ),
    ).toBe("orders-dev is still coming up. Connection details appear once it is ready.");
  });

  it("does not promise a connection to a database that failed", () => {
    expect(
      connectBlockedReason(
        createMockDatabase({ name: "orders-dev", status: "failed", connection: undefined }),
      ),
    ).toBe("orders-dev did not come up, so it published no connection.");
  });

  it("says a database on its way out is going", () => {
    expect(
      connectBlockedReason(
        createMockDatabase({ name: "orders-dev", status: "deleting", connection: undefined }),
      ),
    ).toBe("orders-dev is being deleted.");
  });
});

describe("maskedConnectionUri", () => {
  it("gives a PostgreSQL string the database and the TLS requirement the provider's string carries", () => {
    const database = createMockDatabase({
      engine: "postgresql",
      connection: { host: "host", port: 5432, username: "postgres", secretName: "s" },
    });

    expect(maskedConnectionUri(database)).toBe(
      `postgres://postgres:${passwordMask}@host:5432/postgres?sslmode=require`,
    );
  });

  it("leaves the MySQL shape as it was", () => {
    const database = createMockDatabase({
      engine: "mysql",
      connection: { host: "host", port: 3306, username: "root", secretName: "s" },
    });

    expect(maskedConnectionUri(database)).toBe(`mysql://root:${passwordMask}@host:3306`);
  });
});
