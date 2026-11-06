import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockMetrics, renderWithProviders } from "@/test/utils";
import { overviewMetricKinds } from "../types";
import { OverviewMetrics } from "./overview-metrics";

const id = "spoke-a/pds.orders-dev";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("OverviewMetrics", () => {
  it("shows the three overview minis in their fixed order", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(createMockMetrics([...overviewMetricKinds]))),
    );

    renderWithProviders(<OverviewMetrics databaseId={id} />);

    await waitFor(() =>
      expect(screen.getByTestId("metric-mini-cpu")).toHaveAttribute("data-state", "data"),
    );

    expect(screen.getByTestId("metric-mini-memory")).toBeInTheDocument();
    expect(screen.getByTestId("metric-mini-connections")).toBeInTheDocument();
    expect(screen.queryByTestId("metric-mini-disk")).not.toBeInTheDocument();
  });

  it("feeds all three from one adapter call", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(jsonResponse(createMockMetrics([...overviewMetricKinds])));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<OverviewMetrics databaseId={id} />);

    await waitFor(() =>
      expect(screen.getByTestId("metric-mini-cpu")).toHaveAttribute("data-state", "data"),
    );

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      `metrics=${encodeURIComponent("cpu,memory,connections")}`,
    );
  });

  it("stands pending badges where the charts will be", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse(createMockMetrics([...overviewMetricKinds], { pending: true })),
        ),
    );

    renderWithProviders(<OverviewMetrics databaseId={id} />);

    expect(await screen.findAllByText("Metrics pending")).toHaveLength(3);
  });

  it("states a failed read on every mini", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          jsonResponse({ code: "ErrDownstream", message: "Metrics are not available." }, 502),
        ),
    );

    renderWithProviders(<OverviewMetrics databaseId={id} />);

    expect(await screen.findAllByText("Metrics could not be read")).toHaveLength(3);
  });

  it("shows skeletons while the read is in flight", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderWithProviders(<OverviewMetrics databaseId={id} />);

    expect(screen.getByTestId("metric-mini-cpu")).toHaveAttribute("data-state", "loading");
  });
});
