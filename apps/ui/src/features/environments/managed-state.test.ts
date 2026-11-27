import { describe, expect, it } from "vitest";
import { createMockLifecycle } from "@/test/utils";
import { environmentPill, lifecycleNotes, managedPill, operableEnvironment } from "./managed-state";
import type { Environment } from "./types";

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

describe("environmentPill and operableEnvironment", () => {
  const now = new Date().toISOString();
  it.each<[string, Environment, string, boolean]>([
    [
      "a joined environment that reports",
      { name: "a", status: "connected", lastSeen: now },
      "Connected",
      true,
    ],
    [
      "a joined environment gone quiet",
      { name: "a", status: "unreachable", lastSeen: now },
      "Unreachable",
      true,
    ],
    [
      "a joined environment that never reported",
      { name: "a", status: "unreachable" },
      "Joining",
      false,
    ],
    [
      "a managed environment being built",
      { name: "a", status: "pending", origin: "managed", state: "creating" },
      "Creating",
      false,
    ],
    [
      "a managed environment that is ready",
      { name: "a", status: "connected", origin: "managed", state: "ready" },
      "Ready",
      true,
    ],
    [
      "a managed environment that is lost",
      { name: "a", status: "pending", origin: "managed", state: "lost" },
      "Lost",
      false,
    ],
  ])("reads %s", (_, environment, label, operable) => {
    expect(environmentPill(environment).label).toBe(label);
    expect(operableEnvironment(environment)).toBe(operable);
  });
});
