import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHookWithProviders } from "@/test/utils";
import { useDatabasesCpu } from "./use-databases-cpu";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useDatabasesCpu", () => {
  it("asks for the CPU chart of every running database", async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            metrics: [
              { kind: "cpu", pending: false, points: [{ t: "2026-09-08T12:00:00Z", v: 4 }] },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    renderHookWithProviders(() => useDatabasesCpu(["spoke-a/pds.orders"]));

    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
  });

  // The route answers 501 with metrics off, and a row without a line is what
  // the list draws anyway, so the poll is worth nothing there.
  it("asks for nothing when metrics are turned off", async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(new Response(JSON.stringify({ metrics: [] }), { status: 200 })),
    );
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHookWithProviders(() => useDatabasesCpu(["spoke-a/pds.orders"]), {
      flags: { metrics: false },
    });

    await waitFor(() => expect(result.current).toEqual({}));
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
