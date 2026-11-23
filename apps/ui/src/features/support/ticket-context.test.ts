import { describe, expect, it } from "vitest";
import { createMockDatabase } from "@/test/utils";
import { buildTicketContext, clusterTypeFromMembers } from "./ticket-context";

describe("clusterTypeFromMembers", () => {
  it("returns standalone for one member", () => {
    expect(clusterTypeFromMembers(1)).toBe("standalone");
  });

  it("returns cluster for more than one member", () => {
    expect(clusterTypeFromMembers(3)).toBe("cluster");
  });
});

describe("buildTicketContext", () => {
  it("maps database fields the adapter assembles into a ticket", () => {
    expect(
      buildTicketContext(
        createMockDatabase({
          name: "orders-dev",
          profile: "prod",
          version: "8.4.3",
          members: 3,
          status: "failed",
          message: "No node has enough memory.",
        }),
        { errorMessage: "Pod orders-dev-0 restarted 5 times" },
      ),
    ).toEqual({
      database: "orders-dev",
      profile: "prod",
      engine: "mysql",
      engineVersion: "8.4.3",
      clusterType: "cluster",
      namespace: "pds",
      members: 3,
      status: "failed",
      errorMessage: "Pod orders-dev-0 restarted 5 times",
    });
  });

  it.each(["mysql", "postgresql", "mongodb"] as const)(
    "passes the %s engine through to the ticket",
    (engine) => {
      expect(
        buildTicketContext(createMockDatabase({ engine, version: "8.0.4", members: 3 })),
      ).toMatchObject({ engine, engineVersion: "8.0.4", members: 3 });
    },
  );
});
