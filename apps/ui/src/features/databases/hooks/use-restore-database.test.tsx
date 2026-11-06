import { act, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { databaseKey, useRestoreDatabase } from "@/features/databases";
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

const databaseId = "spoke-a/pds.orders-prod";

describe("useRestoreDatabase", () => {
  it("posts the backup to the restore endpoint", async () => {
    const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(jsonResponse({ status: "accepted" }, 202)),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useRestoreDatabase(databaseId));

    await act(async () => {
      await result.current.mutateAsync({
        backupId: "orders-prod-20260825020000",
        name: "orders-prod-restore-1",
      });
    });

    const [path, init] = fetchMock.mock.calls[0] ?? [];
    expect(String(path)).toBe("/api/databases/spoke-a/pds.orders-prod/restore");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      backupId: "orders-prod-20260825020000",
      name: "orders-prod-restore-1",
    });
  });

  // The phase is what tells the developer a restore is running, so the detail
  // read has to be re-taken rather than left on the cached Ready.
  it("marks the database stale so the phase is re-read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse({ status: "accepted" }, 202))),
    );

    const { result, queryClient } = renderHookWithProviders(() => useRestoreDatabase(databaseId));
    queryClient.setQueryData(databaseKey(databaseId), createMockDatabase({ id: databaseId }));
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");

    await act(async () => {
      await result.current.mutateAsync({
        backupId: "orders-prod-20260825020000",
        name: "orders-prod-restore-1",
      });
    });

    expect(invalidate).toHaveBeenCalledWith({ queryKey: databaseKey(databaseId) });
  });

  it("surfaces the adapter refusal rather than a generic failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          jsonResponse(
            {
              code: "ErrConflict",
              message: "That backup has not finished. Restore once it succeeds.",
            },
            409,
          ),
        ),
      ),
    );

    const { result } = renderHookWithProviders(() => useRestoreDatabase(databaseId));

    act(() => {
      result.current.mutate({
        backupId: "orders-prod-20260825140000",
        name: "orders-prod-restore-1",
      });
    });

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrConflict");
    expect(result.current.error?.message).toBe(
      "That backup has not finished. Restore once it succeeds.",
    );
  });
});
