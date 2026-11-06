import { describe, expect, it } from "vitest";
import {
  activeProvisioningStep,
  buildProvisioningSteps,
  isProvisioningComplete,
} from "@/features/databases";
import type { ProvisioningStepId } from "@/features/databases";
import { createMockDatabase } from "@/test/utils";

function states(steps: ReturnType<typeof buildProvisioningSteps>) {
  return steps.map((step) => [step.id, step.state]);
}

describe("activeProvisioningStep", () => {
  it("starts on volumes while the operator has not claimed the request", () => {
    expect(activeProvisioningStep(createMockDatabase({ status: "pending" }))).toBe("volumes");
    expect(activeProvisioningStep(createMockDatabase({ status: "provisioning" }))).toBe("volumes");
  });

  it("moves to the database once the volumes are bound", () => {
    expect(activeProvisioningStep(createMockDatabase({ status: "initializing" }))).toBe("database");
  });

  it("waits on the app user while the connection secret is unpublished", () => {
    expect(activeProvisioningStep(createMockDatabase({ status: "ready" }))).toBe("app-user");
  });

  it("has no active step once the connection is published", () => {
    expect(
      activeProvisioningStep(
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
    ).toBeNull();
  });
});

describe("isProvisioningComplete", () => {
  it("is complete on ready with a connection", () => {
    expect(
      isProvisioningComplete(
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
    ).toBe(true);
  });

  it("is not complete on ready without a connection", () => {
    expect(isProvisioningComplete(createMockDatabase({ status: "ready" }))).toBe(false);
  });

  it("is not complete on a failure, which is its own state", () => {
    expect(isProvisioningComplete(createMockDatabase({ status: "failed" }))).toBe(false);
  });
});

describe("buildProvisioningSteps", () => {
  it("names the three steps in order", () => {
    const steps = buildProvisioningSteps(createMockDatabase({ status: "pending" }));

    expect(steps.map((step) => step.label)).toEqual([
      "Creating volumes",
      "Starting database",
      "Creating app user",
    ]);
  });

  it("marks earlier steps done and later steps upcoming", () => {
    const steps = buildProvisioningSteps(createMockDatabase({ status: "initializing" }));

    expect(states(steps)).toEqual([
      ["volumes", "done"],
      ["database", "active"],
      ["app-user", "upcoming"],
    ]);
  });

  it("treats unpublished credentials as the app-user step", () => {
    const steps = buildProvisioningSteps(createMockDatabase({ status: "ready" }));

    expect(states(steps)).toEqual([
      ["volumes", "done"],
      ["database", "done"],
      ["app-user", "active"],
    ]);
  });

  it("marks every step done once the connection is published", () => {
    const steps = buildProvisioningSteps(
      createMockDatabase({
        status: "ready",
        connection: {
          host: "orders-dev.percona.svc",
          port: 3306,
          username: "app",
          maskedUri: "mysql://app:••••••••@orders-dev.percona.svc:3306",
        },
      }),
    );

    expect(steps.every((step) => step.state === "done")).toBe(true);
  });

  it("marks the step that was running when the create failed", () => {
    const steps = buildProvisioningSteps(createMockDatabase({ status: "failed" }), {
      failedStep: "volumes",
    });

    expect(states(steps)).toEqual([
      ["volumes", "failed"],
      ["database", "upcoming"],
      ["app-user", "upcoming"],
    ]);
  });

  it("falls back to the database step when the failure arrived before any poll", () => {
    const steps = buildProvisioningSteps(createMockDatabase({ status: "failed" }));

    expect(states(steps)).toEqual([
      ["volumes", "done"],
      ["database", "failed"],
      ["app-user", "upcoming"],
    ]);
  });

  it("attaches the last operator event recorded against each step", () => {
    const events: Partial<Record<ProvisioningStepId, string>> = {
      volumes: "PersistentVolumeClaim orders-dev-pvc bound",
      database: "Waiting for pod orders-dev-0 to become ready",
    };

    const steps = buildProvisioningSteps(createMockDatabase({ status: "initializing" }), {
      events,
    });

    expect(steps[0]?.event).toBe("PersistentVolumeClaim orders-dev-pvc bound");
    expect(steps[1]?.event).toBe("Waiting for pod orders-dev-0 to become ready");
    expect(steps[2]?.event).toBeUndefined();
  });

  describe("an unsettled failure", () => {
    it("keeps the steps running while the operator retries", () => {
      const steps = buildProvisioningSteps(createMockDatabase({ status: "failed" }), {
        failed: false,
        failedStep: "volumes",
      });

      expect(steps.map((step) => step.state)).toEqual(["active", "upcoming", "upcoming"]);
    });

    it("holds the spinner on the step that was running", () => {
      const steps = buildProvisioningSteps(createMockDatabase({ status: "failed" }), {
        failed: false,
        failedStep: "database",
      });

      expect(steps.find((step) => step.state === "active")?.id).toBe("database");
    });

    it("still fails the step once the caller settles the failure", () => {
      const steps = buildProvisioningSteps(createMockDatabase({ status: "failed" }), {
        failed: true,
        failedStep: "volumes",
      });

      expect(steps.map((step) => step.state)).toEqual(["failed", "upcoming", "upcoming"]);
    });
  });
});
