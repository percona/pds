import { describe, expect, it } from "vitest";
import { describeOperationState, describeResourceKind, isOperationActive } from "./operation";

describe("isOperationActive", () => {
  it("keeps a poll alive while the operator is still working", () => {
    for (const state of ["pending", "provisioning", "initializing", "running", "restoring"]) {
      expect(isOperationActive({ kind: "backup", name: "x", state })).toBe(true);
    }
  });

  it("stops on a state that means the operation ended", () => {
    for (const state of ["succeeded", "failed", "ready"]) {
      expect(isOperationActive({ kind: "backup", name: "x", state })).toBe(false);
    }
  });

  it("stops when there is no object to follow", () => {
    expect(isOperationActive(undefined)).toBe(false);
  });
});

describe("describeOperationState", () => {
  it("gives an instance phase and a backup state the same vocabulary", () => {
    expect(describeOperationState("provisioning").label).toBe("Creating volumes");
    expect(describeOperationState("succeeded").kind).toBe("ready");
    expect(describeOperationState("failed").kind).toBe("failed");
  });

  /** A state the adapter adds after this build ships still gets a pill. */
  it("falls back to the raw state", () => {
    expect(describeOperationState("hibernating").label).toBe("hibernating");
  });
});

describe("describeResourceKind", () => {
  it("names the object the way the rest of the app does", () => {
    expect(describeResourceKind("instance")).toBe("Database");
    expect(describeResourceKind("backup")).toBe("Backup");
  });
});
