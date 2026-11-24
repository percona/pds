import { describe, expect, it } from "vitest";
import {
  isRestoreDatabaseName,
  restoreNameRule,
  restoreNameSuggestion,
  restoreSource,
} from "@/features/databases";
import { createMockBackup } from "@/test/utils";

describe("isRestoreDatabaseName", () => {
  it("accepts a DNS label", () => {
    expect(isRestoreDatabaseName("orders-restore-1", "mysql")).toBe(true);
    expect(isRestoreDatabaseName("a", "mysql")).toBe(true);
  });

  // The provider builds its restore job name out of this one twice over, and
  // Kubernetes refuses that label past 63 characters. A longer name reached
  // the environment and came up as an empty database.
  it("refuses a name too long for the provider's restore job", () => {
    expect(isRestoreDatabaseName("a".repeat(restoreNameRule("mysql").limit), "mysql")).toBe(true);
    expect(isRestoreDatabaseName("a".repeat(restoreNameRule("mysql").limit + 1), "mysql")).toBe(
      false,
    );
    expect(isRestoreDatabaseName("dev-testing-restore-2", "mysql")).toBe(false);
  });

  it("refuses anything Kubernetes would not take as a name", () => {
    expect(isRestoreDatabaseName("", "mysql")).toBe(false);
    expect(isRestoreDatabaseName("Orders", "mysql")).toBe(false);
    expect(isRestoreDatabaseName("orders restore", "mysql")).toBe(false);
    expect(isRestoreDatabaseName("orders-", "mysql")).toBe(false);
    expect(isRestoreDatabaseName("../orders", "mysql")).toBe(false);
  });
});

// A PostgreSQL copy is limited by the labels its members carry, not by a
// restore job, so it takes the longer PostgreSQL limit.
describe("restoreNameRule", () => {
  it("lets a PostgreSQL copy take the PostgreSQL name limit", () => {
    expect(restoreNameRule("postgresql").limit).toBe(37);
    expect(isRestoreDatabaseName("a".repeat(37), "postgresql")).toBe(true);
    expect(isRestoreDatabaseName("a".repeat(38), "postgresql")).toBe(false);
    expect(isRestoreDatabaseName("dev-testing-restore-2", "postgresql")).toBe(true);
  });

  it("suggests a PostgreSQL copy name inside the PostgreSQL limit", () => {
    const suggested = restoreNameSuggestion("a".repeat(63), [], "postgresql");

    expect(suggested).toHaveLength(37);
    expect(suggested.endsWith("-restore-1")).toBe(true);
  });
});

// The MongoDB provider refuses a database name over 22 characters, copy or not.
describe("restoreNameRule for MongoDB", () => {
  it("holds a MongoDB copy to the MongoDB name limit", () => {
    expect(restoreNameRule("mongodb").limit).toBe(22);
    expect(isRestoreDatabaseName("a".repeat(22), "mongodb")).toBe(true);
    expect(isRestoreDatabaseName("a".repeat(23), "mongodb")).toBe(false);
  });

  it("suggests a MongoDB copy name inside the MongoDB limit", () => {
    const suggested = restoreNameSuggestion("a".repeat(63), [], "mongodb");

    expect(suggested).toHaveLength(22);
    expect(suggested.endsWith("-restore-1")).toBe(true);
  });
});

describe("restoreNameSuggestion", () => {
  it("offers the source name with a restore suffix", () => {
    expect(restoreNameSuggestion("orders", [], "mysql")).toBe("orders-restore-1");
  });

  // The field opens on a name the adapter will accept, so the first thing a
  // developer sees is not a refusal of something they did not type.
  it("moves past the names the environment already runs", () => {
    expect(restoreNameSuggestion("orders", ["orders", "orders-restore-1"], "mysql")).toBe(
      "orders-restore-2",
    );
  });

  // A suggestion the adapter has to refuse is worse than a truncated one, and
  // the limit here is the provider's restore job rather than the label itself.
  it("stays inside the copy limit for a long source name", () => {
    const source = "a".repeat(63);
    const suggested = restoreNameSuggestion(source, [], "mysql");

    expect(suggested.length).toBeLessThanOrEqual(restoreNameRule("mysql").limit);
    expect(isRestoreDatabaseName(suggested, "mysql")).toBe(true);
    expect(suggested.endsWith("-restore-1")).toBe(true);
  });
});

describe("restoreSource", () => {
  it("says which backup a restore reads from", () => {
    const backup = createMockBackup({
      id: "orders-prod-20260825020000",
      scheduled: true,
      sizeBytes: 2147483648,
    });

    expect(restoreSource(backup)).toBe("Scheduled backup, 2 GB");
  });

  it("names an on-demand backup as one", () => {
    const backup = createMockBackup({ scheduled: false, sizeBytes: 1073741824 });

    expect(restoreSource(backup)).toBe("On-demand backup, 1 GB");
  });

  // A finished backup with no reported size is still restorable, so the size is
  // left out rather than printed as a zero.
  it("leaves the size out when the operator has not reported one", () => {
    const backup = createMockBackup({ scheduled: true, sizeBytes: 0 });

    expect(restoreSource(backup)).toBe("Scheduled backup");
  });
});
