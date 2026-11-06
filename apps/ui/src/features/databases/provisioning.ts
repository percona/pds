import type { Database } from "./types";

export type ProvisioningStepId = "volumes" | "database" | "app-user";

export type ProvisioningStepState = "done" | "active" | "upcoming" | "failed";

export type ProvisioningStep = {
  id: ProvisioningStepId;
  label: string;
  state: ProvisioningStepState;
  /** The last operator event recorded while this step was running. */
  event?: string;
};

/** The last operator event seen for each step, accumulated across polls. */
export type ProvisioningEvents = Partial<Record<ProvisioningStepId, string>>;

/** Steps in the order the operator works through them. */
const stepOrder: readonly { id: ProvisioningStepId; label: string }[] = [
  { id: "volumes", label: "Creating volumes" },
  { id: "database", label: "Starting database" },
  { id: "app-user", label: "Creating app user" },
];

/**
 * The step the operator is on, or null once the database is fully up.
 *
 * The adapter reports a phase, not a step, so the mapping uses the two signals
 * it does publish: the phase, and whether the connection Secret exists.
 * Monitoring is not part of create; metrics stay pending until PMM scrapes.
 */
export function activeProvisioningStep(database: Database): ProvisioningStepId | null {
  switch (database.status) {
    case "pending":
    case "provisioning":
      return "volumes";

    case "ready":
      return database.connection ? null : "app-user";

    default:
      return "database";
  }
}

/** True once the database is up and its connection details are published. */
export function isProvisioningComplete(database: Database): boolean {
  return database.status === "ready" && database.connection != null;
}

type BuildOptions = {
  events?: ProvisioningEvents;
  /**
   * The step that was running when the create failed. The adapter reports no
   * step on a failure, so this comes from what the screen already watched. It
   * falls back to the database step, which is where a create that got past
   * validation usually stops.
   */
  failedStep?: ProvisioningStepId;
  /**
   * Whether the failure has held long enough to report. It defaults to the
   * reported status, so a caller that does not watch the database over time
   * still sees a failure the moment the adapter reports one.
   */
  failed?: boolean;
};

export function buildProvisioningSteps(
  database: Database,
  options: BuildOptions = {},
): ProvisioningStep[] {
  const { events = {}, failedStep, failed = database.status === "failed" } = options;

  if (failed) {
    const failedIndex = indexOf(failedStep ?? "database");

    return stepOrder.map((step, index) => ({
      ...step,
      state: failureState(index, failedIndex),
      event: events[step.id],
    }));
  }

  // A failure that has not settled leaves the spinner on the step that was
  // running, rather than moving it while the operator retries.
  const active =
    database.status === "failed" ? (failedStep ?? "database") : activeProvisioningStep(database);
  const activeIndex = active === null ? stepOrder.length : indexOf(active);

  return stepOrder.map((step, index) => ({
    ...step,
    state: index < activeIndex ? "done" : index === activeIndex ? "active" : "upcoming",
    event: events[step.id],
  }));
}

/** The label the failure state names, for example "Creating volumes failed." */
export function provisioningStepLabel(id: ProvisioningStepId): string {
  return stepOrder[indexOf(id)]?.label ?? id;
}

function failureState(index: number, failedIndex: number): ProvisioningStepState {
  if (index < failedIndex) return "done";
  if (index === failedIndex) return "failed";

  return "upcoming";
}

function indexOf(id: ProvisioningStepId): number {
  return stepOrder.findIndex((step) => step.id === id);
}
