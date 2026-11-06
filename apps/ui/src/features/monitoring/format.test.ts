import { describe, expect, it } from "vitest";
import { formatMetricValue, metricLabel, metricTextEquivalent, trendLabel } from "./format";
import type { MetricSeries } from "./types";

function series(overrides?: Partial<MetricSeries>): MetricSeries {
  return {
    kind: "cpu",
    unit: "percent",
    pending: false,
    current: 31,
    min: 18.5,
    max: 31,
    average: 24.5,
    trend: "up",
    points: [{ t: "2026-08-25T12:00:00Z", v: 31 }],
    ...overrides,
  };
}

describe("formatMetricValue", () => {
  it("renders a percent without fake precision", () => {
    expect(formatMetricValue(31, "percent")).toBe("31%");
    expect(formatMetricValue(24.53, "percent")).toBe("24.5%");
    expect(formatMetricValue(0, "percent")).toBe("0%");
  });

  it("renders a count as a whole number", () => {
    expect(formatMetricValue(14, "count")).toBe("14");
    expect(formatMetricValue(14.6, "count")).toBe("15");
  });

  it("renders seconds with its unit", () => {
    expect(formatMetricValue(0.5, "seconds")).toBe("0.5s");
    expect(formatMetricValue(0, "seconds")).toBe("0s");
    expect(formatMetricValue(42, "seconds")).toBe("42s");
  });
});

describe("metricLabel", () => {
  it("names every chart in sentence case", () => {
    expect(metricLabel("cpu")).toBe("CPU");
    expect(metricLabel("memory")).toBe("Memory");
    expect(metricLabel("connections")).toBe("Connections");
    expect(metricLabel("replication-lag")).toBe("Replication lag");
    expect(metricLabel("disk")).toBe("Disk");
  });
});

describe("trendLabel", () => {
  it("says the direction in words", () => {
    expect(trendLabel("up")).toBe("trending up");
    expect(trendLabel("down")).toBe("trending down");
    expect(trendLabel("steady")).toBe("steady");
  });
});

describe("metricTextEquivalent", () => {
  it("reads a chart out as its current value and trend", () => {
    expect(metricTextEquivalent(series())).toBe("CPU 31%, trending up");
  });

  it("says a chart is pending rather than reading a zero out", () => {
    expect(metricTextEquivalent(series({ pending: true, current: 0, points: [] }))).toBe(
      "CPU metrics pending",
    );
  });

  it("says a window with no readings has none", () => {
    expect(metricTextEquivalent(series({ current: 0, points: [] }))).toBe(
      "CPU has no readings in this window",
    );
  });
});
