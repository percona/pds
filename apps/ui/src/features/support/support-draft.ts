import type { Alert } from "@/features/alerts";
import type { Database } from "@/features/databases/types";
import { buildTicketContext } from "./ticket-context";
import type { TicketContextInput } from "./types";

/** What a case is about, as the page that opens it knows it. */
export type SupportDraftInput = {
  /** Overrides the subject the details would give. */
  subject?: string;
  database?: Database;
  /** The environment, when no database or alert names it. */
  environment?: string;
  alert?: Alert;
  /** What failed, such as the reason a create stopped. */
  errorMessage?: string;
};

export type SupportDraft = {
  subject: string;
  description: string;
  context?: TicketContextInput;
};

/**
 * The prefilled case a Contact support dialog opens with. Database facts travel
 * as the ticket context, which the control plane appends to the description
 * itself, so the description carries only what the context has no field for:
 * the environment and the alert.
 */
export function supportDraft({
  subject,
  database,
  environment,
  alert,
  errorMessage,
}: SupportDraftInput = {}): SupportDraft {
  const where = environment ?? database?.environment ?? alert?.environment;
  const lines: string[] = [];

  if (where) {
    lines.push(`Environment: ${where}`);
  }
  if (alert) {
    if (alert.databaseId && !database) {
      lines.push(`Database: ${alert.databaseId}`);
    }
    lines.push(
      `Alert: ${alert.summary}`,
      `Severity: ${alert.severity}`,
      `Rule: ${alert.rule}`,
      `Firing since: ${alert.startsAt}`,
    );
    const labels = Object.entries(alert.labels ?? {});
    if (labels.length > 0) {
      lines.push("Labels from monitoring:", ...labels.map(([key, value]) => `  ${key}: ${value}`));
    }
  }
  if (errorMessage && !database) {
    lines.push(`Error: ${errorMessage}`);
  }

  return {
    subject: subject ?? defaultSubject(database, alert),
    // A blank line after the facts is where the developer starts typing.
    description: lines.length > 0 ? `${lines.join("\n")}\n\n` : "",
    context: database ? buildTicketContext(database, { errorMessage }) : undefined,
  };
}

function defaultSubject(database: Database | undefined, alert: Alert | undefined): string {
  if (alert) {
    return alert.summary;
  }
  if (database) {
    return `Help with ${database.name}`;
  }

  return "Support request";
}
