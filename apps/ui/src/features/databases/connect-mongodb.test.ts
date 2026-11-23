import { describe, expect, it } from "vitest";
import { createMockDatabase } from "@/test/utils";
import {
  cliCommand,
  forwardedCliCommand,
  maskedConnectionUri,
  passwordFromUri,
  passwordMask,
  portForwardCommand,
} from "./connect";

const members = [
  "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017",
  "orders-rs0-2.orders-rs0.pds.svc.cluster.local:27017",
];
const list = members.join(",");

const mongodb = createMockDatabase({
  name: "orders",
  engine: "mongodb",
  namespace: "pds",
  members: 3,
  connection: {
    host: "orders-rs0-0.orders-rs0.pds.svc.cluster.local",
    port: 27017,
    members,
    username: "databaseAdmin",
    secretName: "orders-conn",
    serviceName: "orders-rs0-0",
  },
});

describe("maskedConnectionUri for MongoDB", () => {
  it("lists every member and names the replica set, with the password masked", () => {
    expect(maskedConnectionUri(mongodb)).toBe(
      `mongodb://databaseAdmin:${passwordMask}@${list}/admin?replicaSet=rs0&ssl=false`,
    );
  });

  it("falls back to the one host and port an older control plane reports", () => {
    const old = createMockDatabase({
      engine: "mongodb",
      connection: { host: "orders-rs0-0.pds", port: 27017, username: "databaseAdmin" },
    });

    expect(maskedConnectionUri(old)).toBe(
      `mongodb://databaseAdmin:${passwordMask}@orders-rs0-0.pds:27017/admin?replicaSet=rs0&ssl=false`,
    );
  });

  it("never holds a password", () => {
    expect(maskedConnectionUri(mongodb)).not.toMatch(/s3cr3t/);
  });
});

describe("cliCommand for MongoDB", () => {
  it("runs mongosh with the whole string", () => {
    expect(cliCommand(mongodb, "s3cr3t")).toBe(
      `mongosh 'mongodb://databaseAdmin:s3cr3t@${list}/admin?replicaSet=rs0&ssl=false'`,
    );
  });

  it("percent-escapes a password a parser would split", () => {
    const command = cliCommand(mongodb, "p@ss:w/rd#?%");

    expect(command).toContain("databaseAdmin:p%40ss%3Aw%2Frd%23%3F%25@");
  });

  it("escapes a single quote so the shell string holds", () => {
    expect(cliCommand(mongodb, "it's")).toContain(`it%27s`);
  });

  it("falls back to the prompting form when there is no password", () => {
    expect(cliCommand(mongodb, "")).toBe(
      `mongosh 'mongodb://databaseAdmin@${list}/admin?replicaSet=rs0&ssl=false'`,
    );
  });

  it("quotes a username that carries shell syntax", () => {
    const injected = createMockDatabase({
      engine: "mongodb",
      connection: { host: "h", port: 27017, members: ["h:27017"], username: "a';id;'" },
    });

    expect(cliCommand(injected, "")).toBe(
      `mongosh 'mongodb://a%27%3Bid%3B%27@h:27017/admin?replicaSet=rs0&ssl=false'`,
    );
  });
});

describe("port-forward for MongoDB", () => {
  it("forwards one member's Service", () => {
    expect(portForwardCommand(mongodb)).toBe(
      "kubectl -n 'pds' port-forward 'svc/orders-rs0-0' '27017:27017'",
    );
  });

  it("connects to the forward directly, because the members advertise names a laptop cannot resolve", () => {
    expect(forwardedCliCommand(mongodb)).toBe(
      "mongosh 'mongodb://databaseAdmin@127.0.0.1:27017/admin?directConnection=true&ssl=false'",
    );
  });
});

describe("the string a copy returns", () => {
  it("gives its password back from the escaped form", () => {
    expect(
      passwordFromUri(
        `mongodb://databaseAdmin:p%40ss%3Aw%2Frd@${list}/admin?replicaSet=rs0&ssl=false`,
      ),
    ).toBe("p@ss:w/rd");
  });
});
