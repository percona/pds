import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { backupsPollMs, backupsRefetchInterval, useBackups } from "@/features/databases";
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

describe("useBackups", () => {
  it("reads the list the adapter returns", async () => {
    const backup = createMockBackup();
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ backups: [backup] }));
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() =>
      useBackups("spoke-a/pds.orders-dev", { enabled: true }),
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([backup]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/databases/spoke-a/pds.orders-dev/backups");
  });

  it("treats a body without the field as no backups", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({})));

    const { result } = renderHookWithProviders(() =>
      useBackups("spoke-a/pds.orders-dev", { enabled: true }),
    );

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([]);
  });

  // The list is one adapter call behind a tab the developer has to open, so it
  // must not run for a tab nobody is looking at.
  it("does not read until the tab asks for it", () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ backups: [] }));
    vi.stubGlobal("fetch", fetchMock);

    renderHookWithProviders(() => useBackups("spoke-a/pds.orders-dev", { enabled: false }));

    expect(fetchMock).not.toHaveBeenCalled();
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

    const { result } = renderHookWithProviders(() =>
      useBackups("spoke-a/pds.orders-dev", { enabled: true }),
    );

    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.error?.code).toBe("ErrDownstream");
  });
});

describe("backupsRefetchInterval", () => {
  it("polls while a backup is still running", () => {
    expect(backupsRefetchInterval([createMockBackup({ status: "running" })])).toBe(backupsPollMs);
  });

  it("stops once every backup has an outcome", () => {
    expect(
      backupsRefetchInterval([
        createMockBackup({ status: "succeeded" }),
        createMockBackup({ id: "orders-dev-2", status: "failed" }),
      ]),
    ).toBe(false);
  });

  it("does not poll an empty list", () => {
    expect(backupsRefetchInterval([])).toBe(false);
  });

  it("polls before the first list arrives", () => {
    expect(backupsRefetchInterval(undefined)).toBe(backupsPollMs);
  });
});
