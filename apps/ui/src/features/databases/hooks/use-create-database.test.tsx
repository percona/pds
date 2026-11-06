import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { databaseKey, useCreateDatabase } from "@/features/databases";
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

describe("useCreateDatabase", () => {
  it("posts the create input and returns the database the adapter made", async () => {
    const created = createMockDatabase({
      id: "pds.orders-dev",
      name: "orders-dev",
      status: "provisioning",
    });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(created, 201));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useCreateDatabase());
    result.current.mutate({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      environment: "spoke-a",
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual(created);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases");
    const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      environment: "spoke-a",
    });
  });

  it("surfaces the adapter message when the name is taken", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(
            { code: "ErrConflict", message: "A database named `orders-dev` already exists." },
            409,
          ),
        ),
    );

    const { result } = renderHookWithProviders(() => useCreateDatabase());
    result.current.mutate({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      environment: "spoke-a",
    });

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrConflict");
    expect(result.current.error?.message).toBe("A database named `orders-dev` already exists.");
  });

  it("seeds the cache so the provisioning screen starts from the create response", async () => {
    const created = createMockDatabase({
      id: "pds.orders-dev",
      name: "orders-dev",
      status: "pending",
    });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(created, 201)));

    const { result, queryClient } = renderHookWithProviders(() => useCreateDatabase());
    result.current.mutate({
      name: "orders-dev",
      engine: "mysql",
      profile: "dev",
      environment: "spoke-a",
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(queryClient.getQueryData(databaseKey("pds.orders-dev"))).toEqual(created);
  });
});
