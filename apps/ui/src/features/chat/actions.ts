import { allMetricKinds } from "@/features/monitoring";
import type { MetricKind } from "@/features/monitoring";
import type { NavigableIntent } from "@/features/databases";
import type { ChatAction } from "./types";

export type ResolvedChatAction =
  | { type: "navigate"; databaseId: string; intent: NavigableIntent }
  | {
      type: "support";
      intent: Extract<NavigableIntent, { kind: "support" }>;
      databaseId?: string;
      databaseName?: string;
    };

export function databaseNameFromId(id: string): string {
  const segment = id.split("/").pop() ?? id;
  const parts = segment.split(".");
  return parts[parts.length - 1] ?? segment;
}

function toIntent(action: ChatAction, assistantText: string): NavigableIntent | null {
  switch (action.kind) {
    case "metrics": {
      const metric = action.metric as MetricKind | undefined;
      if (!metric || !allMetricKinds.includes(metric)) {
        return null;
      }

      return { kind: "metrics", metric };
    }
    case "status":
      return { kind: "status" };
    case "support": {
      const subject = action.subject?.trim() || assistantText;
      const description = action.description?.trim() || assistantText;

      return { kind: "support", subject, description };
    }
    default:
      return null;
  }
}

/**
 * Turns one chat action into what the page can perform. A button only opens
 * something: the database, its metrics, or a support ticket. A change is made
 * by a tool call the person approves, so a scale, backup or schedule button an
 * older reply saved is dropped, with every unknown kind.
 */
export function resolveChatAction(
  action: ChatAction,
  assistantText: string,
): ResolvedChatAction | null {
  const intent = toIntent(action, assistantText);
  if (!intent) {
    return null;
  }

  if (intent.kind === "support") {
    if (!action.id) {
      return { type: "support", intent };
    }

    return {
      type: "support",
      intent,
      databaseId: action.id,
      databaseName: databaseNameFromId(action.id),
    };
  }

  if (!action.id) {
    return null;
  }

  return { type: "navigate", databaseId: action.id, intent };
}
