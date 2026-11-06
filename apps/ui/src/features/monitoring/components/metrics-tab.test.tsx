import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockMetrics, renderWithProviders } from "@/test/utils";
import { allMetricKinds } from "../types";
import { MetricsTab } from "./metrics-tab";

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

describe("MetricsTab", () => {
  it("shows skeletons shaped like the charts while it loads", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );

    renderWithProviders(<MetricsTab databaseId={id} />);

    expect(screen.getByTestId("metrics-loading")).toBeInTheDocument();
  });

  it("draws the five metric charts in their fixed order", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds))),
    );

    renderWithProviders(<MetricsTab databaseId={id} />);

    await screen.findByTestId("metric-panel-cpu");

    const panels = screen
      .getAllByTestId(/^metric-panel-/)
      .map((panel) => panel.getAttribute("data-kind"));

    expect(panels).toEqual(["cpu", "memory", "connections", "replication-lag", "disk"]);
  });

  it("gives every chart a text equivalent", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds))),
    );

    renderWithProviders(<MetricsTab databaseId={id} />);

    const panel = within(await screen.findByTestId("metric-panel-cpu"));

    expect(panel.getByText("CPU")).toBeInTheDocument();
    expect(panel.getByTestId("metric-current")).toHaveTextContent("31%");
    expect(panel.getByText("trending up")).toBeInTheDocument();
    expect(panel.getByRole("img", { name: "CPU 31%, trending up" })).toBeInTheDocument();
  });

  it("reports the range of each chart as text", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds))),
    );

    renderWithProviders(<MetricsTab databaseId={id} />);

    const panel = within(await screen.findByTestId("metric-panel-cpu"));

    const summary = (name: string) => panel.getByTestId(`metric-summary-${name}`);

    expect(summary("min")).toHaveTextContent("Low");
    expect(summary("min")).toHaveTextContent("18.5%");
    expect(summary("max")).toHaveTextContent("High");
    expect(summary("max")).toHaveTextContent("31%");
    expect(summary("average")).toHaveTextContent("Average");
    expect(summary("average")).toHaveTextContent("24.5%");
  });

  it("asks the adapter for the other window when the toggle changes", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds)));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<MetricsTab databaseId={id} />);

    await screen.findByTestId("metric-panel-cpu");

    const hour = screen.getByRole("button", { name: "1h" });
    const day = screen.getByRole("button", { name: "24h" });

    expect(hour).toHaveAttribute("aria-pressed", "true");
    expect(day).toHaveAttribute("aria-pressed", "false");

    await userEvent.click(day);

    await waitFor(() => expect(day).toHaveAttribute("aria-pressed", "true"));

    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    expect(urls.some((url) => url.includes("window=24h"))).toBe(true);
  });

  it("stands pending badges where the charts will be", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse(
          createMockMetrics(allMetricKinds, {
            pending: true,
            pendingReason: "PMM has no target for this database yet.",
          }),
        ),
      ),
    );

    renderWithProviders(<MetricsTab databaseId={id} />);

    await screen.findByTestId("metric-panel-cpu");

    expect(screen.getAllByText("Metrics pending")).toHaveLength(5);
    expect(screen.getByText("PMM has no target for this database yet.")).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
  });

  it("explains a failed read and offers one retry", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        jsonResponse(
          { code: "ErrDownstream", message: "Metrics are not available right now." },
          502,
        ),
      );
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<MetricsTab databaseId={id} />);

    expect(await screen.findByText("Metrics could not be read")).toBeInTheDocument();
    expect(screen.getByText("Metrics are not available right now.")).toBeInTheDocument();

    fetchMock.mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds)));
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.getByTestId("metric-panel-cpu")).toBeInTheDocument());
  });

  it("says which charts have no readings without calling it a failure", async () => {
    const snapshot = createMockMetrics(allMetricKinds);
    const gap = snapshot.metrics.find((series) => series.kind === "replication-lag");

    if (gap) {
      gap.current = 0;
      gap.points = [];
    }

    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(snapshot)));

    renderWithProviders(<MetricsTab databaseId={id} />);

    const panel = within(await screen.findByTestId("metric-panel-replication-lag"));

    expect(panel.getByText("No readings in this window")).toBeInTheDocument();
    expect(screen.queryByText("Metrics pending")).not.toBeInTheDocument();
  });

  it("asks for every chart in one call", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(createMockMetrics(allMetricKinds)));
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(<MetricsTab databaseId={id} />);

    await screen.findByTestId("metric-panel-cpu");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      `metrics=${encodeURIComponent("cpu,memory,connections,replication-lag,disk")}`,
    );
  });
});
