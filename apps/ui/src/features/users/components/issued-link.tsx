import { Check, Copy, ExternalLink, TriangleAlert, X } from "lucide-react";
import { useId } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { InviteOutcome, IssuedInvitation } from "../types";

/** An invite outcome the page keeps until it is dismissed. */
export type ListedOutcome = InviteOutcome & { key: number };

type IssuedLinksProps = {
  outcomes: ListedOutcome[];
  onDismiss: (key: number) => void;
};

/**
 * Says what became of each address of an invite and shows each link. The
 * control plane keeps only a hash of a link, so this is the one chance to copy
 * it, and the links stay on the page until each is dismissed.
 */
export function IssuedLinks({ outcomes, onDismiss }: IssuedLinksProps) {
  const headingId = useId();

  if (outcomes.length === 0) {
    return null;
  }

  return (
    <Card role="status" aria-labelledby={headingId} className="overflow-hidden">
      <h2 id={headingId} className="px-4 pt-4 text-base font-semibold">
        {outcomes.length > 1 ? "Invite links" : "Invite link"}
      </h2>
      <ul className="divide-y divide-border">
        {outcomes.map((outcome) =>
          outcome.kind === "issued" ? (
            <IssuedRow
              key={outcome.key}
              invitation={outcome.invitation}
              onDismiss={() => onDismiss(outcome.key)}
            />
          ) : (
            <li key={outcome.key} className="flex items-start gap-2 p-4 text-sm">
              <TriangleAlert
                className="mt-0.5 size-4 shrink-0 text-warning"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <p className="min-w-0 flex-1">
                <span className="font-mono">{outcome.email || "Anyone with the link"}</span>. Not
                sent: {outcome.reason}
              </p>
              <DismissButton
                who={outcome.email || "anyone"}
                onDismiss={() => onDismiss(outcome.key)}
              />
            </li>
          ),
        )}
      </ul>
      <p className="border-t border-border px-4 py-3 text-xs text-muted-foreground">
        A link is shown once, because only a hash of it is kept. Resend an invite to get a new one.
        Opening a link here does not use it.
      </p>
    </Card>
  );
}

function IssuedRow({
  invitation,
  onDismiss,
}: {
  invitation: IssuedInvitation;
  onDismiss: () => void;
}) {
  const token = tokenOf(invitation.link);

  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(invitation.link);
      toast.success("Link copied");
    } catch {
      toast.error("The browser blocked the copy. Select the link and copy it.");
    }
  }

  return (
    <li className="space-y-3 p-4">
      <div className="flex items-start gap-2">
        <p className="flex min-w-0 flex-1 items-start gap-2 text-sm font-medium">
          <Check
            className="mt-0.5 size-4 shrink-0 text-success"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          {headline(invitation)}
        </p>
        <DismissButton who={invitation.email || "anyone"} onDismiss={onDismiss} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <code className="min-w-0 flex-1 basis-48 truncate rounded-md border border-border bg-muted px-3 py-2 font-mono text-xs">
          {invitation.link}
        </code>
        <Button variant="outline" onClick={() => void copy()}>
          <Copy className="size-4" strokeWidth={1.5} aria-hidden="true" />
          Copy link
        </Button>
        {token === null ? null : (
          // The claim screen of this console, not the link's own host, so an
          // admin sees exactly what the person will see without leaving it.
          <Button variant="ghost" asChild>
            <Link to={`/claim?token=${encodeURIComponent(token)}`} target="_blank" rel="noreferrer">
              <ExternalLink className="size-4" strokeWidth={1.5} aria-hidden="true" />
              Open as the invited person
            </Link>
          </Button>
        )}
      </div>
    </li>
  );
}

function DismissButton({ who, onDismiss }: { who: string; onDismiss: () => void }) {
  return (
    <Button
      variant="ghost"
      size="icon"
      className="-mt-1 shrink-0"
      aria-label={`Dismiss the invite for ${who}`}
      onClick={onDismiss}
    >
      <X className="size-4" strokeWidth={1.5} aria-hidden="true" />
    </Button>
  );
}

function tokenOf(link: string): string | null {
  try {
    return new URL(link).searchParams.get("token");
  } catch {
    return null;
  }
}

function headline({ email, emailed }: IssuedInvitation): string {
  if (email === undefined || email === "") {
    return "Invite created for anyone. Send them this link.";
  }

  return emailed
    ? `Invite sent to ${email}.`
    : `Invite created for ${email}. It was not emailed, so send them this link.`;
}
