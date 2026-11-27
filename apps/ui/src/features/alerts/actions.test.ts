import { describe, expect, it } from "vitest";
import { createMockAlert } from "@/test/utils";
import { alertActionSummary, alertActionTarget, alertTarget } from "./actions";
import type { AlertActionKind } from "./types";

const databaseId = "spoke-a/pds.orders-dev";
const databasePath = "/databases/spoke-a/pds.orders-dev";

function target(kind: AlertActionKind, id?: string) {
  return alertActionTarget({ kind, label: "x" }, "spoke-a", id);
}

describe("alertActionTarget", () => {
  it("opens the database for open_database", () => {
    expect(target("open_database", databaseId)).toEqual({ to: databasePath });
  });

  it("opens the database on the storage dialog for scale_storage", () => {
    expect(target("scale_storage", databaseId)).toEqual({
      to: databasePath,
      state: { chatIntent: { kind: "scale", field: "storage" } },
    });
  });

  it("opens the database on the resources dialog for scale_resources", () => {
    expect(target("scale_resources", databaseId)).toEqual({
      to: databasePath,
      state: { chatIntent: { kind: "scale", field: "memory" } },
    });
  });

  it("opens the replication lag chart for view_metrics", () => {
    expect(target("view_metrics", databaseId)).toEqual({
      to: databasePath,
      state: { chatIntent: { kind: "metrics", metric: "replication-lag" } },
    });
  });

  it("opens the Backups tab for retry_backup and starts nothing", () => {
    expect(target("retry_backup", databaseId)).toEqual({
      to: databasePath,
      state: { chatIntent: { kind: "backups" } },
    });
  });

  it("opens the environment for view_environment", () => {
    expect(target("view_environment", undefined)).toEqual({ to: "/environments/spoke-a" });
  });

  it.each(["set_up_monitoring", "open_monitoring"] as const)(
    "opens the environment the alert names for %s",
    (kind) => {
      expect(target(kind, undefined)).toEqual({ to: "/environments/spoke-a" });
    },
  );

  it("opens the update page for the environment for update_connection", () => {
    expect(target("update_connection", undefined)).toEqual({
      to: "/environments/spoke-a/update",
    });
    expect(
      alertActionTarget({ kind: "update_connection", label: "Update connection" }, "a b"),
    ).toEqual({ to: "/environments/a%20b/update" });
  });

  it("carries the Team into the update page for update_connection", () => {
    expect(
      alertActionTarget(
        { kind: "update_connection", label: "Update connection" },
        "prod",
        undefined,
        "beta",
      ),
    ).toEqual({ to: "/environments/prod/update?team=beta" });
    expect(
      alertTarget(
        createMockAlert({
          environment: "prod",
          action: { kind: "update_connection", label: "Update connection" },
        }),
        "beta",
      ),
    ).toEqual({ to: "/environments/prod/update?team=beta" });
  });

  it("falls back to the environment when a database action names no database", () => {
    expect(target("open_database", undefined)).toEqual({ to: "/environments/spoke-a" });
    expect(target("scale_storage", undefined)).toEqual({ to: "/environments/spoke-a" });
  });

  it("falls back to the environment for a kind it does not know", () => {
    expect(alertActionTarget({ kind: "later" as AlertActionKind, label: "x" }, "a b")).toEqual({
      to: "/environments/a%20b",
    });
  });

  it("reads the action and database off an alert", () => {
    const alert = createMockAlert({ action: { kind: "scale_storage", label: "Scale storage" } });

    expect(alertTarget(alert).to).toBe(databasePath);
  });
});

describe("alertActionSummary", () => {
  it("says the storage action opens the database where Scale changes storage", () => {
    const alert = createMockAlert({ action: { kind: "scale_storage", label: "Scale storage" } });

    expect(alertActionSummary(alert)).toBe("Opens the database, where Scale changes its storage.");
  });

  it("says a retry only opens the Backups tab and changes nothing", () => {
    const alert = createMockAlert({ action: { kind: "retry_backup", label: "Retry backup" } });

    expect(alertActionSummary(alert)).toMatch(/so following this link changes nothing/);
  });

  it("says the environment opens when the alert names no database", () => {
    const alert = createMockAlert({
      databaseId: undefined,
      action: { kind: "scale_storage", label: "Scale storage" },
    });

    expect(alertActionSummary(alert)).toBe("Opens the environment.");
  });
});
