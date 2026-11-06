import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api";
import { createMockMetricSeries, renderWithProviders } from "@/test/utils";
import { MetricMini } from "./metric-mini";

describe("MetricMini", () => {
  it("shows a skeleton shaped like the loaded card while it loads", () => {
    renderWithProviders(<MetricMini kind="cpu" loading />);

    const mini = screen.getByTestId("metric-mini-cpu");

    expect(mini).toHaveAttribute("data-state", "loading");
    expect(within(mini).getByText("CPU")).toBeInTheDocument();
  });

  it("stands a pending badge where the chart will be", () => {
    renderWithProviders(
      <MetricMini
        kind="cpu"
        series={createMockMetricSeries("cpu", { pending: true, current: 0, points: [] })}
      />,
    );

    const mini = screen.getByTestId("metric-mini-cpu");

    expect(mini).toHaveAttribute("data-state", "pending");
    expect(within(mini).getByText("Metrics pending")).toBeInTheDocument();
  });

  it("treats a missing series as pending rather than as zero", () => {
    renderWithProviders(<MetricMini kind="memory" />);

    expect(screen.getByTestId("metric-mini-memory")).toHaveAttribute("data-state", "pending");
  });

  it("reads the current value and the trend as text next to the chart", () => {
    renderWithProviders(<MetricMini kind="cpu" series={createMockMetricSeries("cpu")} />);

    const mini = screen.getByTestId("metric-mini-cpu");

    expect(mini).toHaveAttribute("data-state", "data");
    expect(within(mini).getByText("31%")).toBeInTheDocument();
    expect(within(mini).getByText("trending up")).toBeInTheDocument();
    expect(within(mini).getByRole("img", { name: "CPU 31%, trending up" })).toBeInTheDocument();
  });

  it("formats each unit the way the adapter reports it", () => {
    renderWithProviders(
      <>
        <MetricMini
          kind="connections"
          series={createMockMetricSeries("connections", { current: 14 })}
        />
        <MetricMini
          kind="replication-lag"
          series={createMockMetricSeries("replication-lag", { current: 0.5 })}
        />
      </>,
    );

    expect(within(screen.getByTestId("metric-mini-connections")).getByText("14")).toBeVisible();
    expect(
      within(screen.getByTestId("metric-mini-replication-lag")).getByText("0.5s"),
    ).toBeVisible();
  });

  it("says a window with no readings has none instead of drawing a flat line", () => {
    renderWithProviders(
      <MetricMini
        kind="replication-lag"
        series={createMockMetricSeries("replication-lag", { current: 0, points: [] })}
      />,
    );

    const mini = screen.getByTestId("metric-mini-replication-lag");

    expect(mini).toHaveAttribute("data-state", "empty");
    expect(within(mini).getByText("No readings in this window")).toBeInTheDocument();
  });

  it("states a failed read instead of showing an empty card", () => {
    renderWithProviders(
      <MetricMini
        kind="cpu"
        error={new ApiError("ErrDownstream", "Metrics are not available right now.", 502)}
      />,
    );

    const mini = screen.getByTestId("metric-mini-cpu");

    expect(mini).toHaveAttribute("data-state", "error");
    expect(within(mini).getByText("Metrics could not be read")).toBeInTheDocument();
  });

  it("numbers are mono", () => {
    renderWithProviders(<MetricMini kind="cpu" series={createMockMetricSeries("cpu")} />);

    expect(screen.getByText("31%")).toHaveClass("font-mono");
  });
});
