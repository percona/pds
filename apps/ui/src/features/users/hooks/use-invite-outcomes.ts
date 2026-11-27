import { useCallback, useRef, useState } from "react";
import type { ListedOutcome } from "../components/issued-link";
import type { InviteOutcome, IssuedInvitation } from "../types";

/**
 * The invite outcomes a page shows until each is dismissed. A link cannot be
 * read back from the control plane, so this page state is the only place it
 * lives. A resend replaces the link it made stale.
 */
export function useInviteOutcomes() {
  const [outcomes, setOutcomes] = useState<ListedOutcome[]>([]);
  const next = useRef(0);

  const add = useCallback((added: InviteOutcome[]) => {
    const listed = added.map((outcome) => ({ ...outcome, key: next.current++ }));
    const replaced = new Set(
      added.flatMap((outcome) => (outcome.kind === "issued" ? [outcome.invitation.id] : [])),
    );
    setOutcomes((current) => [
      ...listed,
      ...current.filter(
        (outcome) => outcome.kind !== "issued" || !replaced.has(outcome.invitation.id),
      ),
    ]);
  }, []);

  const addIssued = useCallback(
    (invitation: IssuedInvitation) => add([{ kind: "issued", invitation }]),
    [add],
  );

  const dismiss = useCallback((key: number) => {
    setOutcomes((current) => current.filter((outcome) => outcome.key !== key));
  }, []);

  return { outcomes, add, addIssued, dismiss };
}
