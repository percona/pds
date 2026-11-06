import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { databasesPollMs, databasesRefetchInterval, useDatabases } from "@/features/databases";
import { createMockDatabase, renderHookWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useDatabases", () => {
  it("reads the list the adapter returns", async () => {
    const database = createMockDatabase({ id: "pds.orders-dev" });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ databases: [database] }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDatabases());

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([database]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases");
  });

  it("treats a body without the field as no databases", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({})));

    const { result } = renderHookWithProviders(() => useDatabases());

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([]);
  });

  it("surfaces an adapter failure instead of an empty list", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(
            { code: "ErrDownstream", message: "The database platform did not answer." },
            502,
          ),
        ),
    );

    const { result } = renderHookWithProviders(() => useDatabases());

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrDownstream");
  });
});

describe("databasesRefetchInterval", () => {
  it("polls while any database is still moving", () => {
    expect(
      databasesRefetchInterval([
        createMockDatabase({ status: "ready" }),
        createMockDatabase({ id: "pds.two", status: "provisioning" }),
      ]),
    ).toBe(databasesPollMs);
  });

  it("stops once every database has settled", () => {
    expect(
      databasesRefetchInterval([
        createMockDatabase({
          status: "ready",
          connection: {
            host: "orders-dev.percona.svc",
            port: 3306,
            username: "app",
            maskedUri: "mysql://app:••••••••@orders-dev.percona.svc:3306",
          },
        }),
        createMockDatabase({ id: "pds.two", status: "failed" }),
      ]),
    ).toBe(false);
  });

  it("polls before the first list arrives", () => {
    expect(databasesRefetchInterval(undefined)).toBe(databasesPollMs);
  });
});
