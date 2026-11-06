import { describe, expect, it } from "vitest";
import { resolveChatAction } from "./actions";

describe("resolveChatAction", () => {
  const assistantText = "CPU is consistently high.";

  it("opens the database for status and metrics", () => {
    expect(
      resolveChatAction(
        { label: "View status", kind: "status", id: "pds.orders-dev" },
        assistantText,
      ),
    ).toEqual({
      type: "navigate",
      databaseId: "pds.orders-dev",
      intent: { kind: "status" },
    });
    expect(
      resolveChatAction(
        { label: "View metrics", kind: "metrics", metric: "cpu", id: "pds.orders-dev" },
        assistantText,
      ),
    ).toEqual({
      type: "navigate",
      databaseId: "pds.orders-dev",
      intent: { kind: "metrics", metric: "cpu" },
    });
  });

  it.each([
    { label: "Scale up", kind: "scale", field: "cpu", id: "pds.orders-dev" },
    { label: "Retry backup", kind: "backup", id: "pds.orders-dev" },
    { label: "Enable schedule", kind: "schedule", id: "pds.orders-dev" },
  ])(
    "drops the $kind button an older reply saved, since a change is asked for in the chat",
    (action) => {
      expect(resolveChatAction(action, assistantText)).toBeNull();
    },
  );

  it("drops unknown action kinds", () => {
    expect(resolveChatAction({ label: "Restart", kind: "restart" }, assistantText)).toBeNull();
  });

  it("keeps support on the chat page when no database id is present", () => {
    expect(resolveChatAction({ label: "Contact support", kind: "support" }, assistantText)).toEqual(
      {
        type: "support",
        intent: {
          kind: "support",
          subject: assistantText,
          description: assistantText,
        },
      },
    );
  });

  it("uses action subject and description for support when provided", () => {
    expect(
      resolveChatAction(
        {
          label: "Contact support",
          kind: "support",
          subject: "Disk full",
          description: "Volume at 95 percent",
        },
        assistantText,
      ),
    ).toEqual({
      type: "support",
      intent: {
        kind: "support",
        subject: "Disk full",
        description: "Volume at 95 percent",
      },
    });
  });

  it("includes the database name from the id for support with context", () => {
    expect(
      resolveChatAction(
        { label: "Contact support", kind: "support", id: "pds.orders-dev" },
        assistantText,
      ),
    ).toEqual({
      type: "support",
      databaseId: "pds.orders-dev",
      databaseName: "orders-dev",
      intent: {
        kind: "support",
        subject: assistantText,
        description: assistantText,
      },
    });
  });
});
