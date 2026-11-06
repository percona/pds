import { afterEach, describe, expect, it, vi } from "vitest";
import { createDatabase, databaseResourcePath } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("createDatabase", () => {
  const input = {
    name: "orders",
    engine: "mysql",
    profile: "prod",
    environment: "eks-1",
  } as const;

  function stubFetch() {
    const fetchMock = vi.fn((_input: RequestInfo | URL) =>
      Promise.resolve(new Response("{}", { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    return fetchMock;
  }

  it("creates in the Team it is given", async () => {
    const fetchMock = stubFetch();

    await createDatabase(input, "Payments");

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/databases?team=Payments");
  });

  it("follows the acting Team when it is given none", async () => {
    const fetchMock = stubFetch();

    await createDatabase(input);

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/databases");
  });
});

describe("databaseResourcePath", () => {
  it("rejects an id that is not an environment and a name", () => {
    expect(() => databaseResourcePath("pds.orders-dev")).toThrow(/environment and a name/);
  });

  it("splits a two-segment id so the adapter can bind environment and name", () => {
    expect(databaseResourcePath("eks-testing/pds.dev-testing")).toBe("eks-testing/pds.dev-testing");
  });

  it("encodes each segment", () => {
    expect(databaseResourcePath("spoke a/pds.orders")).toBe("spoke%20a/pds.orders");
  });

  it("refuses a segment that would escape the path", () => {
    expect(() => databaseResourcePath("ns/../platform.orders")).toThrow(/environment and a name/);
  });
});
