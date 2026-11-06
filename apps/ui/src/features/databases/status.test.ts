import { describe, expect, it } from "vitest";
import { databaseStatus, statusKinds } from "@/features/databases";
import type { DatabasePhase } from "@/features/databases";
import { createMockDatabase } from "@/test/utils";

const phases: DatabasePhase[] = [
  "pending",
  "provisioning",
  "initializing",
  "ready",
  "updating",
  "deleting",
  "failed",
  "restoring",
  "suspending",
  "suspended",
  "resuming",
  "unknown",
];

describe("databaseStatus", () => {
  it("names the five pills the design system ships", () => {
    expect(
      databaseStatus(
        createMockDatabase({
          status: "ready",
          connection: {
            host: "orders-dev.percona.svc",
            port: 3306,
            username: "app",
            maskedUri: "mysql://app:••••••••@orders-dev.percona.svc:3306",
          },
        }),
      ),
    ).toEqual({
      kind: "ready",
      label: "Ready",
    });
    expect(databaseStatus(createMockDatabase({ status: "provisioning" }))).toEqual({
      kind: "creating",
      label: "Creating volumes",
    });
    expect(databaseStatus(createMockDatabase({ status: "failed" }))).toEqual({
      kind: "failed",
      label: "Failed",
    });
    expect(databaseStatus(createMockDatabase({ status: "unknown" }))).toEqual({
      kind: "degraded",
      label: "Degraded",
    });
  });

  it("keeps the work in progress style but says which work", () => {
    expect(databaseStatus(createMockDatabase({ status: "pending" }))).toEqual({
      kind: "creating",
      label: "Queued",
    });
    expect(databaseStatus(createMockDatabase({ status: "initializing" }))).toEqual({
      kind: "creating",
      label: "Starting database",
    });
    expect(databaseStatus(createMockDatabase({ status: "updating" }))).toEqual({
      kind: "creating",
      label: "Updating",
    });
    expect(databaseStatus(createMockDatabase({ status: "restoring" }))).toEqual({
      kind: "creating",
      label: "Restoring",
    });
    expect(databaseStatus(createMockDatabase({ status: "suspended" }))).toEqual({
      kind: "degraded",
      label: "Suspended",
    });
  });

  it("maps every phase the adapter can report onto a shipped pill", () => {
    for (const status of phases) {
      const { kind, label } = databaseStatus(createMockDatabase({ status }));

      expect(statusKinds).toContain(kind);
      expect(label).not.toBe("");
    }
  });

  it("keeps polling while the connection secret is unpublished", () => {
    expect(databaseStatus(createMockDatabase({ status: "ready" }))).toEqual({
      kind: "creating",
      label: "Publishing connection",
    });
  });
});
