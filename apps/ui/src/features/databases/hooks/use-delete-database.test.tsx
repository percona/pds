import { act, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { backupsKey, databaseKey, databasesKey, useDeleteDatabase } from "@/features/databases";
import { createMockBackup, createMockDatabase, renderHookWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function noContent() {
  return new Response(null, { status: 204 });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

const databaseId = "spoke-a/pds.orders-prod";

describe("useDeleteDatabase", () => {
  it("sends DELETE to the database with no body", async () => {
    const fetchMock = vi.fn().mockResolvedValue(noContent());
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDeleteDatabase(databaseId));

    await act(async () => {
      await result.current.mutateAsync();
    });

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(path).toBe("/api/databases/spoke-a/pds.orders-prod");
    expect(init.method).toBe("DELETE");
    expect(init.body).toBeUndefined();
  });

  // The database is gone, so its reads are dropped rather than invalidated:
  // re-taking them would only produce a 404 for a page that has left.
  it("drops the reads of a database that no longer exists", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(noContent()));

    const { result, queryClient } = renderHookWithProviders(() => useDeleteDatabase(databaseId));
    queryClient.setQueryData(databaseKey(databaseId), createMockDatabase({ id: databaseId }));
    queryClient.setQueryData(backupsKey(databaseId), [createMockBackup()]);

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(queryClient.getQueryData(databaseKey(databaseId))).toBeUndefined();
    expect(queryClient.getQueryData(backupsKey(databaseId))).toBeUndefined();
  });

  it("marks the list stale so the row goes with it", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(noContent()));

    const { result, queryClient } = renderHookWithProviders(() => useDeleteDatabase(databaseId));
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: databasesKey });
  });

  it("surfaces the adapter refusal rather than a generic failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ code: "ErrNotFound", message: "No database named `orders-prod`." }, 404),
        ),
    );

    const { result } = renderHookWithProviders(() => useDeleteDatabase(databaseId));

    act(() => {
      result.current.mutate();
    });

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrNotFound");
    expect(result.current.error?.message).toBe("No database named `orders-prod`.");
  });
});
