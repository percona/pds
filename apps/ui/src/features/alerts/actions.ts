import { databaseHref } from "@/features/databases";
import { updatePath } from "@/features/environments";
import type { NavigableIntent } from "@/features/databases";
import type { Alert, AlertAction } from "./types";

export type AlertLinkTarget = {
  to: string;
  /** Router state, which the database page reads as a request to open a control. */
  state?: { chatIntent: NavigableIntent };
};

/**
 * Where an action leads. A database action opens the database page on the
 * control that does the work, the way a chat action does. Retrying a backup
 * opens the Backups tab and leaves the button to the customer: following a
 * link never writes. An action about a database the alert does not name, or
 * one the console has no page for (monitoring setup among them), opens the
 * environment, which is the closest page that exists. Updating a connection
 * opens the environment's update page, which prints the command to run.
 */
export function alertActionTarget(
  action: AlertAction,
  environment: string,
  databaseId?: string,
  team?: string,
): AlertLinkTarget {
  const environmentTo = `/environments/${encodeURIComponent(environment)}`;

  switch (action.kind) {
    case "open_database":
      return databaseId ? { to: databaseHref(databaseId) } : { to: environmentTo };
    case "scale_storage":
      return databaseTarget(databaseId, environmentTo, { kind: "scale", field: "storage" });
    case "scale_resources":
      return databaseTarget(databaseId, environmentTo, { kind: "scale", field: "memory" });
    case "view_metrics":
      return databaseTarget(databaseId, environmentTo, {
        kind: "metrics",
        metric: "replication-lag",
      });
    case "retry_backup":
      return databaseTarget(databaseId, environmentTo, { kind: "backups" });
    case "update_connection":
      return { to: updatePath(environment, team) };
    default:
      return { to: environmentTo };
  }
}

function databaseTarget(
  databaseId: string | undefined,
  environmentTo: string,
  chatIntent: NavigableIntent,
): AlertLinkTarget {
  return databaseId
    ? { to: databaseHref(databaseId), state: { chatIntent } }
    : { to: environmentTo };
}

/**
 * The target of the action an alert carries. `team` is the Team the console
 * acts in, which is the Team the alert was read for, so the update page can tell
 * two Teams' environments of the same name apart.
 */
export function alertTarget(alert: Alert, team?: string): AlertLinkTarget {
  return alertActionTarget(alert.action, alert.environment, alert.databaseId, team);
}
