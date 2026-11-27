import { describe, expect, it } from "vitest";
import { createMockAlert, createMockDatabase } from "@/test/utils";
import { supportDraft } from "./support-draft";

describe("supportDraft", () => {
  it("sends a database's facts as context rather than repeating them in the text", () => {
    const database = createMockDatabase({ name: "orders-dev", environment: "spoke-a" });

    const draft = supportDraft({
      subject: "Cannot create database orders-dev",
      database,
      errorMessage: "Pods failed to schedule",
    });

    expect(draft.subject).toBe("Cannot create database orders-dev");
    expect(draft.description).toBe("Environment: spoke-a\n\n");
    expect(draft.context).toMatchObject({
      database: "orders-dev",
      errorMessage: "Pods failed to schedule",
    });
  });

  it("writes an alert into the text, since the context has no field for it", () => {
    const draft = supportDraft({ alert: createMockAlert({ labels: { service: "mysql" } }) });

    expect(draft.subject).toBe("orders-dev is down");
    expect(draft.description).toContain("Alert: orders-dev is down\nSeverity: critical");
    expect(draft.description).toContain("Labels from monitoring:\n  service: mysql");
    expect(draft.context).toBeUndefined();
  });

  it("names the environment alone when that is all the page knows", () => {
    const draft = supportDraft({ environment: "spoke-b", errorMessage: "Join timed out" });

    expect(draft).toEqual({
      subject: "Support request",
      description: "Environment: spoke-b\nError: Join timed out\n\n",
      context: undefined,
    });
  });
});
