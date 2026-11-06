import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchDatabaseMetrics } from "./api";
import type { MetricsSnapshot } from "./types";

const id = "spoke-a/pds.orders-dev";

const snapshot: MetricsSnapshot = {
  databaseId: id,
  window: "1h",
  start: "2026-08-25T11:00:00Z",
  end: "2026-08-25T12:00:00Z",
  stepSeconds: 60,
  pending: false,
  metrics: [
    {
      kind: "cpu",
      unit: "percent",
      pending: false,
      current: 31,
      min: 18.5,
      max: 31,
      average: 24.5,
      trend: "up",
      points: [
        { t: "2026-08-25T11:58:00Z", v: 18.5 },
        { t: "2026-08-25T11:59:00Z", v: 24 },
        { t: "2026-08-25T12:00:00Z", v: 31 },
      ],
    },
  ],
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

describe("fetchDatabaseMetrics", () => {
  it("asks for the window and the charts the caller needs", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(snapshot));
    vi.stubGlobal("fetch", fetchMock);

    await fetchDatabaseMetrics(id, { window: "24h", kinds: ["cpu", "memory"] });

    const [url] = fetchMock.mock.calls[0] as [string];

    expect(url).toContain("/api/databases/spoke-a/pds.orders-dev/metrics");
    expect(url).toContain("window=24h");
    expect(url).toContain(`metrics=${encodeURIComponent("cpu,memory")}`);
  });

  it("returns the snapshot the adapter sent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(snapshot)));

    const got = await fetchDatabaseMetrics(id, { window: "1h", kinds: ["cpu"] });

    expect(got.pending).toBe(false);
    expect(got.metrics).toHaveLength(1);
    expect(got.metrics[0]?.points).toHaveLength(3);
  });

  it("keeps the pending snapshot, which is not an error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          ...snapshot,
          pending: true,
          pendingReason: "PMM has no target for this database yet.",
          metrics: [{ ...snapshot.metrics[0], pending: true, points: [] }],
        }),
      ),
    );

    const got = await fetchDatabaseMetrics(id, { window: "1h", kinds: ["cpu"] });

    expect(got.pending).toBe(true);
    expect(got.pendingReason).toBe("PMM has no target for this database yet.");
  });

  it("reports an adapter error", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ code: "ErrDownstream", message: "Metrics are not available." }, 502),
        ),
    );

    await expect(fetchDatabaseMetrics(id, { window: "1h", kinds: ["cpu"] })).rejects.toThrow(
      "Metrics are not available.",
    );
  });

  // A 200 that is not a snapshot means /api is not proxied, and index.html is
  // not something to draw a chart from.
  it("refuses a body that is not a snapshot", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({ hello: "world" })));

    await expect(fetchDatabaseMetrics(id, { window: "1h", kinds: ["cpu"] })).rejects.toThrow(
      "could not be read",
    );
  });
});
