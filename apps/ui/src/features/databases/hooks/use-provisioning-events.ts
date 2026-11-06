import { useEffect, useState } from "react";
import { activeProvisioningStep } from "../provisioning";
import type { ProvisioningEvents, ProvisioningStepId } from "../provisioning";
import type { Database } from "../types";

export type ProvisioningHistory = {
  events: ProvisioningEvents;
  /** The step that was running at the last poll, so a failure can name it. */
  lastActiveStep?: ProvisioningStepId;
  /** True once a failed status has held long enough to be worth showing. */
  failureSettled: boolean;
};

/**
 * How long a failed status has to hold before the screen calls a create lost.
 * The operator reports Failed while a reconcile retries, and clears it within
 * a cycle, so reporting the first one would show a failure for a database that
 * is still on its way up.
 */
export const failureSettleMs = 30_000;

/**
 * Keeps the last operator message seen against each step. The adapter reports
 * one message for the whole database, so the history is what makes a completed
 * step expandable to the event that finished it.
 */
export function useProvisioningEvents(database?: Database): ProvisioningHistory {
  const [history, setHistory] = useState<Omit<ProvisioningHistory, "failureSettled">>({
    events: {},
  });
  const [failureSettled, setFailureSettled] = useState(false);
  const failed = database?.status === "failed";

  // A failure only counts once it has held. Anything shorter is the operator
  // retrying, and the next poll reports the database on its way again.
  useEffect(() => {
    if (!failed) {
      setFailureSettled(false);

      return;
    }

    const timer = setTimeout(() => setFailureSettled(true), failureSettleMs);

    return () => clearTimeout(timer);
  }, [failed]);

  useEffect(() => {
    // A failure has no step of its own: what matters is the step that was
    // running when it arrived, which is already in the history.
    if (!database || database.status === "failed") {
      return;
    }

    const step = activeProvisioningStep(database);
    if (!step) {
      return;
    }

    const message = database.message;

    setHistory((current) => {
      if (current.lastActiveStep === step && current.events[step] === message) {
        return current;
      }

      return {
        lastActiveStep: step,
        events: message ? { ...current.events, [step]: message } : current.events,
      };
    });
  }, [database]);

  return { ...history, failureSettled };
}
