import { describe, expect, it } from "vitest";
import { connectProgress } from "./progress";
import type { AwsAccount, AwsAccountStep } from "./types";

const since = new Date("2026-11-20T10:00:00Z");

function account(overrides: Partial<AwsAccount> = {}): AwsAccount {
  return {
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state: "pending",
    allowedRegions: ["eu-west-1"],
    stackRegion: "eu-west-1",
    templateVersion: 1,
    latestTemplateVersion: 1,
    createdAt: "2026-11-20T10:00:05Z",
    environments: [],
    ...overrides,
  };
}

function step(name: string, status: AwsAccountStep["status"], at = "2026-11-20T10:00:10Z") {
  return { step: name, status, at };
}

function statuses(progress: ReturnType<typeof connectProgress>) {
  return progress.phases.map((phase) => `${phase.name}: ${phase.status}`);
}

describe("connectProgress", () => {
  it("waits for the command to find the account", () => {
    const progress = connectProgress(undefined, since);

    expect(statuses(progress)).toEqual([
      "Find the AWS account: running",
      "Create the roles: pending",
      "Check that PDS can use them: pending",
    ]);
    expect(progress.done).toBe(false);
  });

  it("shows the roles being created once the account is known", () => {
    const progress = connectProgress(account({ steps: [step("stack", "started")] }), since);

    expect(statuses(progress)).toEqual([
      "Find the AWS account: succeeded",
      "Create the roles: running",
      "Check that PDS can use them: pending",
    ]);
  });

  it("checks the roles after the stack and its protection are done", () => {
    const progress = connectProgress(
      account({
        steps: [
          step("template", "succeeded"),
          step("stack", "succeeded"),
          step("protect", "succeeded"),
          step("verify", "started"),
        ],
      }),
      since,
    );

    expect(statuses(progress)[1]).toBe("Create the roles: succeeded");
    expect(statuses(progress)[2]).toBe("Check that PDS can use them: running");
    expect(progress.done).toBe(false);
  });

  it("is done when a check since the command made the account ready", () => {
    // The Ready check revokes the command's token, so the command's own
    // "verify succeeded" never arrives and its last signal is minutes old.
    const progress = connectProgress(
      account({
        state: "ready",
        checkedAt: "2026-11-20T10:03:00Z",
        signalAt: "2026-11-20T10:02:59Z",
        steps: [
          step("stack", "succeeded"),
          step("protect", "succeeded"),
          step("verify", "started"),
        ],
      }),
      since,
      new Date("2026-11-20T10:10:00Z"),
    );

    expect(progress.done).toBe(true);
    expect(progress.stopped).toBe(false);
    expect(progress.phases.every((phase) => phase.status === "succeeded")).toBe(true);
  });

  it("is not done by a Ready check from before the command was made", () => {
    const progress = connectProgress(
      account({
        state: "ready",
        checkedAt: "2026-11-20T09:00:00Z",
        steps: [step("verify", "started")],
      }),
      since,
    );

    expect(progress.done).toBe(false);
  });

  it("does not take the steps of an earlier run for this one", () => {
    const progress = connectProgress(
      account({
        state: "ready",
        signalAt: "2026-11-20T10:00:06Z",
        steps: [step("verify", "succeeded", "2026-11-19T09:00:00Z")],
      }),
      since,
    );

    expect(progress.done).toBe(false);
  });

  it("stops at the step that failed", () => {
    const progress = connectProgress(
      account({ steps: [step("template", "succeeded"), step("stack", "failed")] }),
      since,
    );

    expect(statuses(progress)[1]).toBe("Create the roles: failed");
    expect(progress.failed).toBe(true);
  });

  it("says the command stopped reporting after a silence", () => {
    const progress = connectProgress(
      account({ signalAt: "2026-11-20T10:00:20Z", steps: [step("stack", "started")] }),
      since,
      new Date("2026-11-20T10:05:00Z"),
    );

    expect(progress.stopped).toBe(true);
  });

  it("does not call a command that just reported stopped", () => {
    const progress = connectProgress(
      account({ signalAt: "2026-11-20T10:00:20Z", steps: [step("stack", "started")] }),
      since,
      new Date("2026-11-20T10:00:40Z"),
    );

    expect(progress.stopped).toBe(false);
  });
});
