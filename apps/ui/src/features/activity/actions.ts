import {
  ArrowDownToLine,
  Ban,
  CalendarClock,
  LifeBuoy,
  Mail,
  UserCheck,
  UserPlus,
  UserX,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  DatabaseBackup,
  Bot,
  KeyRound,
  MessageSquare,
  Plug,
  Plus,
  PlugZap,
  Scaling,
  Trash2,
  Unplug,
  type LucideIcon,
} from "lucide-react";
import type { BadgeProps } from "@/components/ui/badge";

type ActionDescription = {
  label: string;
  icon: LucideIcon;
};

/**
 * Action labels are the verbs the buttons use, not past tense sentences. A row
 * pairs the label with its outcome badge, so "Back up" plus "Failed" reads
 * correctly where "Backed up" plus "Failed" would contradict itself.
 */
const actions: Record<string, ActionDescription> = {
  "database.create": { label: "Create database", icon: Plus },
  "database.scale": { label: "Scale database", icon: Scaling },
  "database.backup": { label: "Back up", icon: DatabaseBackup },
  "database.backup-schedule": { label: "Enable backup schedule", icon: CalendarClock },
  "database.restore": { label: "Restore", icon: ArrowDownToLine },
  "database.delete": { label: "Delete database", icon: Trash2 },
  "connection.update": { label: "Update connection", icon: PlugZap },
  "connection.delete": { label: "Remove connection", icon: Unplug },
  "monitoring.password-read": { label: "Copy monitoring password", icon: KeyRound },
  "database.credential-read": { label: "Read connection string", icon: KeyRound },
  "agent.revoke": { label: "Revoke agent", icon: Ban },
  "invitation.create": { label: "Invite person", icon: UserPlus },
  "invitation.resend": { label: "Resend invite", icon: Mail },
  "invitation.revoke": { label: "Revoke invite", icon: UserX },
  "invitation.claim": { label: "Claim invite", icon: UserCheck },
  "invitation.refuse": { label: "Refuse invite", icon: Ban },
  "support_access.open": { label: "Open support access", icon: LifeBuoy },
  "support_access.extend": { label: "Extend support access", icon: LifeBuoy },
  "support_access.end": { label: "End support access", icon: LifeBuoy },
  "support_access.install": { label: "Install support access", icon: LifeBuoy },
  "support_access.uninstall": { label: "Remove support access", icon: LifeBuoy },
  "support_access.fail": { label: "Support access failed", icon: LifeBuoy },
  "support_access.expire": { label: "Support access ended", icon: LifeBuoy },
  "support_access.remove": { label: "Remove support access", icon: LifeBuoy },
};

/**
 * An action the adapter added after this build shipped still gets a row: the
 * raw value is more use to a reader than a blank cell.
 */
export function describeAction(action: string): ActionDescription {
  return actions[action] ?? { label: action, icon: CircleDashed };
}

const clients: Record<string, { label: string; icon: LucideIcon }> = {
  mcp: { label: "MCP server", icon: Plug },
  chat: { label: "Chat", icon: MessageSquare },
};

/**
 * Who made the request, when it was not a person in the UI. A client declared
 * by an adapter newer than this build shows as recorded, and an entry with no
 * client shows nothing.
 */
export function describeClient(client: string | undefined): string | null {
  if (!client) {
    return null;
  }

  return clients[client]?.label ?? client;
}

/** The icon beside a client, so the kind reads without its text. */
export function clientIcon(client: string | undefined): LucideIcon {
  return (client ? clients[client]?.icon : undefined) ?? Bot;
}

/**
 * The client and the agent behind it on one line, such as "Chat (Anthropic)".
 * An agent that signed in with no client kind shows alone.
 */
export function describeSource(
  client: string | undefined,
  agent: string | undefined,
): string | null {
  const kind = describeClient(client);
  if (kind && agent) {
    return `${kind} (${agent})`;
  }

  return kind ?? (agent || null);
}

type OutcomeDescription = {
  label: string;
  icon: LucideIcon;
  variant: BadgeProps["variant"];
};

const outcomes: Record<string, OutcomeDescription> = {
  succeeded: { label: "Succeeded", icon: CircleCheck, variant: "success" },
  accepted: { label: "Accepted", icon: CircleDashed, variant: "muted" },
  failed: { label: "Failed", icon: CircleAlert, variant: "destructive" },
};

/**
 * Actions that finish inside the request they were made in. For these the
 * recorded outcome is the outcome of the work, because there is no work left
 * once the call returns.
 */
const synchronousActions = new Set<string>([
  "connection.update",
  "connection.delete",
  "monitoring.password-read",
  "database.credential-read",
  "agent.revoke",
  "invitation.create",
  "invitation.resend",
  "invitation.revoke",
  "invitation.claim",
  "invitation.refuse",
  "support_access.open",
  "support_access.extend",
  "support_access.end",
  "support_access.install",
  "support_access.uninstall",
  "support_access.fail",
  "support_access.expire",
  "support_access.remove",
]);

/**
 * What the row is entitled to claim.
 *
 * A recorded success means the platform took the request. Everything that
 * hands work to the operator is still running at that point, and it can fail
 * minutes later, so a row that called that "Succeeded" put a green tick on a
 * create that never came up. Those read as accepted until the timeline behind
 * the row is opened, which is where the live state of the object is read.
 *
 * A failure needs no such care: a request the platform rejected did fail, and
 * no work was started to contradict it.
 */
export function describeOutcome(outcome: string, action?: string): OutcomeDescription {
  const shown = shownOutcome(outcome, action);

  return outcomes[shown] ?? { label: shown, icon: CircleDashed, variant: "muted" };
}

/**
 * The outcome a row shows, which is what the outcome filter matches on, so a
 * filter for Succeeded never lists a row that reads Accepted.
 */
export function shownOutcome(outcome: string, action?: string): string {
  // The adapter now records "accepted" for the work it only started, so this
  // remap is for rows written before it did. The activity ring keeps months of
  // them and they must not start reading as finished work.
  const asynchronous = action !== undefined && !synchronousActions.has(action);

  return outcome === "succeeded" && asynchronous ? "accepted" : outcome;
}
