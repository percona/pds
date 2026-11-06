import { act, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { backupsKey, useBackups, useCreateBackup } from "@/features/databases";
import { createMockBackup, renderHookWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

const databaseId = "spoke-a/pds.orders-dev";

describe("useCreateBackup", () => {
  it("posts to the backup endpoint", async () => {
    const started = createMockBackup({ id: "orders-dev-new", status: "pending", sizeBytes: 0 });
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(started, 202));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useCreateBackup(databaseId));

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/spoke-a/pds.orders-dev/backup");
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: "POST" });
  });

  // The developer pressed a button and has to see something happen, so the row
  // is on screen before the next poll rather than after it.
  it("puts the new backup at the top of the cached list", async () => {
    const started = createMockBackup({ id: "orders-dev-new", status: "pending", sizeBytes: 0 });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(started, 202)));

    const { result, queryClient } = renderHookWithProviders(() => useCreateBackup(databaseId));
    queryClient.setQueryData(backupsKey(databaseId), [createMockBackup({ id: "orders-dev-old" })]);

    await act(async () => {
      await result.current.mutateAsync();
    });

    expect(queryClient.getQueryData(backupsKey(databaseId))).toEqual([
      started,
      createMockBackup({ id: "orders-dev-old" }),
    ]);
  });

  it("surfaces the adapter conflict rather than a generic failure", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          {
            code: "ErrConflict",
            message: "A backup of `orders-dev` is already starting.",
          },
          409,
        ),
      ),
    );

    const { result } = renderHookWithProviders(() => useCreateBackup(databaseId));

    act(() => {
      result.current.mutate();
    });

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrConflict");
  });
});

describe("useBackups after a backup starts", () => {
  it("reads the seeded row without another call", async () => {
    const started = createMockBackup({ id: "orders-dev-new", status: "pending", sizeBytes: 0 });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(started, 202)));

    const { result } = renderHookWithProviders(() => ({
      start: useCreateBackup(databaseId),
      list: useBackups(databaseId, { enabled: false }),
    }));

    await act(async () => {
      await result.current.start.mutateAsync();
    });

    await waitFor(() => expect(result.current.list.data).toEqual([started]));
  });
});
