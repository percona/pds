import { describe, expect, it } from "vitest";
import { createMockLifecycle } from "@/test/utils";
import { lifecycleNotes, managedPill } from "./managed-state";

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

describe("lifecycleNotes", () => {
  it("says nothing when there is nothing to do", () => {
    expect(lifecycleNotes(createMockLifecycle())).toEqual([]);
  });

  it("names an update and a change made outside PDS in words", () => {
    const notes = lifecycleNotes(
      createMockLifecycle({
        pendingChartVersion: "1.5.0",
        drift: [{ resource: "NodeGroup", detail: "Instance type changed" }],
      }),
    );

    expect(notes.map((note) => note.label)).toEqual(["Update available", "Changed outside PDS"]);
  });

  it("counts a template, Kubernetes or PDS update as one update", () => {
    expect(lifecycleNotes(createMockLifecycle({ templateOutOfDate: true }))).toHaveLength(1);
    expect(lifecycleNotes(createMockLifecycle({ kubernetesUpgradeAvailable: true }))).toHaveLength(
      1,
    );
  });
});
