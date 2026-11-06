import { CircleCheck, CircleDot, Clock, TriangleAlert } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { BadgeProps } from "@/components/ui/badge";

/** How many days a ticket waiting on a customer reply stays open. */
export const autoCloseDays = 7;

export type TicketStateStyle = {
  label: string;
  icon: LucideIcon;
  variant: BadgeProps["variant"];
  /** True while the ball is in the developer's court, which starts the clock. */
  awaitingCustomer: boolean;
};

/**
 * How one ServiceNow state reads in the list. The states are configured on the
 * Percona side and can be renamed there, so this matches on what the word says
 * rather than on a fixed set, and an unrecognized state still renders as icon
 * plus its own text.
 */
export function describeTicketState(state: string | undefined): TicketStateStyle {
  const label = (state ?? "").trim();
  const normalized = label.toLowerCase();

  if (label === "") {
    return { label: "Unknown", icon: CircleDot, variant: "muted", awaitingCustomer: false };
  }

  if (normalized.includes("resolved") || normalized.includes("closed")) {
    return { label, icon: CircleCheck, variant: "success", awaitingCustomer: false };
  }

  if (normalized.includes("customer") || normalized.includes("awaiting info")) {
    return { label, icon: TriangleAlert, variant: "warning", awaitingCustomer: true };
  }

  return { label, icon: Clock, variant: "outline", awaitingCustomer: false };
}
