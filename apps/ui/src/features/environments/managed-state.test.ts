import { describe, expect, it } from "vitest";
import { managedPill } from "./managed-state";

describe("managedPill", () => {
  it.each([
    ["creating", "Creating", "creating"],
    ["deleting", "Deleting", "creating"],
    ["failed", "Failed", "failed"],
    ["degraded", "Degraded", "degraded"],
    ["ready", "Ready", "ready"],
  ] as const)("says %s in words", (state, label, kind) => {
    expect(managedPill(state)).toEqual({ kind, label });
  });
});
