import { describe, expect, it } from "vitest";
import { maxActivityEntries, mergeActivity } from "./merge";
import type { ActivityEntry } from "./types";

function entry(id: string, overrides: Partial<ActivityEntry> = {}): ActivityEntry {
  return {
    id,
    at: "2026-03-09T10:30:00Z",
    actor: "unknown",
    action: "database.backup",
    target: "orders",
    outcome: "succeeded",
    ...overrides,
  };
}

describe("mergeActivity", () => {
  it("prepends what a poll returned", () => {
    const merged = mergeActivity([entry("000002"), entry("000001")], [entry("000003")]);

    expect(merged.map((row) => row.id)).toEqual(["000003", "000002", "000001"]);
  });

  it("keeps the list newest first whatever order the answer arrives in", () => {
    const merged = mergeActivity([], [entry("000001"), entry("000003"), entry("000002")]);

    expect(merged.map((row) => row.id)).toEqual(["000003", "000002", "000001"]);
  });

  it("returns the same rows when a poll finds nothing new", () => {
    const existing = [entry("000002"), entry("000001")];

    expect(mergeActivity(existing, [])).toBe(existing);
  });

  // A filter change racing a poll can replay a row that is already on screen.
  it("does not duplicate a row that arrives twice", () => {
    const merged = mergeActivity([entry("000001")], [entry("000001"), entry("000002")]);

    expect(merged.map((row) => row.id)).toEqual(["000002", "000001"]);
  });

  it("prefers the incoming copy of a row it already had", () => {
    const merged = mergeActivity(
      [entry("000001", { outcome: "succeeded" })],
      [entry("000001", { outcome: "failed" })],
    );

    expect(merged).toHaveLength(1);
    expect(merged[0].outcome).toBe("failed");
  });

  it("trims to the cap, dropping the oldest", () => {
    // Ids are zero padded by the adapter, which is what lets a string compare
    // order them.
    const existing = ["000005", "000004", "000003", "000002", "000001"].map((id) => entry(id));

    const merged = mergeActivity(existing, [entry("000006")], 3);

    expect(merged.map((row) => row.id)).toEqual(["000006", "000005", "000004"]);
  });

  it("trims an over long list even when nothing new arrived", () => {
    const existing = ["000004", "000003", "000002", "000001"].map((id) => entry(id));

    expect(mergeActivity(existing, [], 2)).toHaveLength(2);
  });

  it("defaults the cap to the ring the adapter keeps", () => {
    expect(maxActivityEntries).toBe(500);
  });
});
