import { describe, expect, it } from "vitest";
import { describeAction, describeClient, describeOutcome } from "./actions";

describe("describeAction", () => {
  it("labels every action the adapter records", () => {
    const labels = [
      "database.create",
      "database.scale",
      "database.backup",
      "database.backup-schedule",
      "database.restore",
      "database.delete",
      "connection.update",
      "connection.delete",
      "monitoring.password-read",
      "database.credential-read",
    ].map((action) => describeAction(action).label);

    expect(labels).toEqual([
      "Create database",
      "Scale database",
      "Back up",
      "Enable backup schedule",
      "Restore",
      "Delete database",
      "Update connection",
      "Remove connection",
      "Copy monitoring password",
      "Read connection string",
    ]);
  });

  // An adapter newer than the UI must not render blank rows.
  it("falls back to the raw value for an action it does not know", () => {
    expect(describeAction("database.hibernate").label).toBe("database.hibernate");
  });
});

describe("describeOutcome", () => {
  it("pairs an icon and a variant with each outcome", () => {
    expect(describeOutcome("succeeded")).toMatchObject({
      label: "Succeeded",
      variant: "success",
    });
    expect(describeOutcome("failed")).toMatchObject({ label: "Failed", variant: "destructive" });
  });

  it("falls back for an outcome it does not know", () => {
    expect(describeOutcome("partial")).toMatchObject({ label: "partial", variant: "muted" });
  });

  it("reports work the platform only accepted as accepted", () => {
    for (const action of [
      "database.create",
      "database.scale",
      "database.backup",
      "database.backup-schedule",
      "database.restore",
      "database.delete",
    ]) {
      expect(describeOutcome("succeeded", action)).toMatchObject({
        label: "Accepted",
        variant: "muted",
      });
    }
  });

  it("keeps succeeded for an action that finishes inside the request", () => {
    expect(describeOutcome("succeeded", "connection.update")).toMatchObject({
      label: "Succeeded",
      variant: "success",
    });
    expect(describeOutcome("succeeded", "connection.delete")).toMatchObject({
      label: "Succeeded",
      variant: "success",
    });
  });

  it("keeps failed as failed, because a rejected request did fail", () => {
    expect(describeOutcome("failed", "database.create")).toMatchObject({
      label: "Failed",
      variant: "destructive",
    });
  });

  it("treats an action it does not know as asynchronous", () => {
    expect(describeOutcome("succeeded", "database.hibernate")).toMatchObject({
      label: "Accepted",
      variant: "muted",
    });
  });
});

describe("describeOutcome and the accepted outcome", () => {
  it("renders the adapter's accepted outcome with a proper label", () => {
    expect(describeOutcome("accepted", "database.backup").label).toBe("Accepted");
  });

  // The ring keeps months of rows written before the adapter recorded
  // "accepted", and those must not start reading as finished work.
  it("still reads an old asynchronous success as accepted", () => {
    expect(describeOutcome("succeeded", "database.backup").label).toBe("Accepted");
  });

  it("leaves a synchronous success alone", () => {
    expect(describeOutcome("succeeded", "connection.update").label).toBe("Succeeded");
  });

  it("keeps a failure a failure", () => {
    expect(describeOutcome("failed", "database.backup").label).toBe("Failed");
  });
});

describe("describeClient", () => {
  it("names the kinds the adapter records", () => {
    expect(describeClient("mcp")).toBe("MCP server");
  });

  it("names the Chat", () => {
    expect(describeClient("chat")).toBe("Chat");
  });

  it("shows a kind this build does not know as it was recorded", () => {
    expect(describeClient("terraform")).toBe("terraform");
  });

  it("says nothing when the sign in did not declare a client", () => {
    expect(describeClient(undefined)).toBeNull();
    expect(describeClient("")).toBeNull();
  });
});
