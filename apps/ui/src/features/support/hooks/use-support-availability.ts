import { publicSupportPortalUrl } from "../portal";
import { useSupportTickets } from "./use-support-tickets";

export type SupportAvailability = {
  /** True once the adapter has answered with something other than a 501. */
  connected: boolean;
  /** True while the first read is still in flight. */
  checking: boolean;
  /** Where to send a developer this environment cannot open a ticket for. */
  portalUrl: string;
};

/**
 * Whether this environment can open a support ticket. Only a 501 hides the support
 * actions: that is the adapter stating it has no ServiceNow Secret. An adapter
 * that could not be reached leaves them in place, because a network blip is not
 * a reason to tell a developer support does not exist.
 */
export function useSupportAvailability(): SupportAvailability {
  const { data, error, isPending } = useSupportTickets();

  const notConnected = error?.code === "ErrNotImplemented";

  return {
    connected: !notConnected,
    checking: isPending,
    portalUrl: data?.portalUrl ?? publicSupportPortalUrl,
  };
}
