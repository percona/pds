import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { provisioningPollMs, provisioningRefetchInterval, useDatabase } from "@/features/databases";
import { createMockDatabase, renderHookWithProviders } from "@/test/utils";

const connection = {
  host: "orders-dev.pds.svc",
  port: 3306,
  username: "app",
  maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useDatabase", () => {
  it("reads one database from the adapter", async () => {
    const database = createMockDatabase({ id: "spoke-a/pds.orders-dev" });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(database));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDatabase("spoke-a/pds.orders-dev"));

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual(database);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/spoke-a/pds.orders-dev");
  });

  it("refuses an id that would escape the path", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(jsonResponse(createMockDatabase())));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDatabase("ns/../platform.orders"));

    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("opens a hub database with a two-segment path", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(createMockDatabase({ id: "eks-testing/pds.dev-testing" })));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDatabase("eks-testing/pds.dev-testing"));

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/eks-testing/pds.dev-testing");
  });

  it("surfaces a missing database as an adapter error", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ code: "ErrNotFound", message: "No database named `orders-dev`." }, 404),
        ),
    );

    const { result } = renderHookWithProviders(() => useDatabase("spoke-a/pds.orders-dev"));

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrNotFound");
  });
});

describe("provisioningRefetchInterval", () => {
  it("polls while the database is still coming up", () => {
    expect(provisioningRefetchInterval(createMockDatabase({ status: "provisioning" }))).toBe(
      provisioningPollMs,
    );
    expect(provisioningRefetchInterval(createMockDatabase({ status: "ready" }))).toBe(
      provisioningPollMs,
    );
    expect(provisioningRefetchInterval(undefined)).toBe(provisioningPollMs);
  });

  it("stops once the connection is published", () => {
    expect(provisioningRefetchInterval(createMockDatabase({ status: "ready", connection }))).toBe(
      false,
    );
  });

  it("keeps polling on a failure, because the operator clears transient ones", () => {
    expect(provisioningRefetchInterval(createMockDatabase({ status: "failed" }))).toBe(
      provisioningPollMs,
    );
  });
});
