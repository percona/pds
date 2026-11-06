import { describe, expect, it } from "vitest";
import { createMockAlert } from "@/test/utils";
import { summarizeAlerts } from "./summary";

describe("summarizeAlerts", () => {
  it("keeps the highest severity and counts the alerts of that severity", () => {
    const summaries = summarizeAlerts([
      createMockAlert({ id: "a", severity: "warning" }),
      createMockAlert({ id: "b", severity: "critical" }),
      createMockAlert({ id: "c", severity: "critical" }),
      createMockAlert({ id: "d", severity: "info" }),
    ]);

    expect(summaries["spoke-a/pds.orders-dev"]).toEqual({
      total: 4,
      worst: "critical",
      worstCount: 2,
    });
  });

  it("groups by database and skips an alert with no database", () => {
    const summaries = summarizeAlerts([
      createMockAlert({ id: "a", databaseId: "spoke-a/pds.one", severity: "warning" }),
      createMockAlert({ id: "b", databaseId: "spoke-a/pds.two", severity: "info" }),
      createMockAlert({ id: "c", databaseId: undefined }),
    ]);

    expect(Object.keys(summaries).toSorted()).toEqual(["spoke-a/pds.one", "spoke-a/pds.two"]);
    expect(summaries["spoke-a/pds.one"]).toEqual({ total: 1, worst: "warning", worstCount: 1 });
  });

  it("has no entry for a quiet database", () => {
    expect(summarizeAlerts([])).toEqual({});
  });
});
