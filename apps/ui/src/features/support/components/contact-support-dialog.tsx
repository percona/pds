import { CircleAlert, CircleCheck, ExternalLink } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useCreateTicket } from "../hooks/use-create-ticket";
import { publicSupportPortalUrl } from "../portal";
import type { TicketContextInput } from "../types";

type ContactSupportDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  subject: string;
  description: string;
  /** The database facts the adapter appends to the description. */
  context?: TicketContextInput;
  /**
   * False when the adapter answered 501, which means this environment has no
   * ServiceNow Secret. Sending is then not offered at all and the dialog points
   * at the public portal instead.
   */
  connected?: boolean;
  /** The portal to link to. Defaults to the public Percona support page. */
  portalUrl?: string;
};

/**
 * The support ticket dialog. It opens pre-filled with the context a developer
 * would otherwise retype, both fields stay editable, and a send that fails
 * leaves every word of it in place.
 */
export function ContactSupportDialog({
  open,
  onOpenChange,
  subject,
  description,
  context,
  connected = true,
  portalUrl = publicSupportPortalUrl,
}: ContactSupportDialogProps) {
  const fieldId = useId();
  const [draftSubject, setDraftSubject] = useState(subject);
  const [draftDescription, setDraftDescription] = useState(description);
  const send = useCreateTicket();

  // Reopening on a different failure has to show that failure, not the last
  // one, and must not open on the result of the ticket sent before it.
  useEffect(() => {
    if (open) {
      setDraftSubject(subject);
      setDraftDescription(description);
      send.reset();
    }
  }, [open, subject, description, send.reset]);

  const created = send.data;
  const sendable = draftSubject.trim() !== "" && draftDescription.trim() !== "";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Contact support</DialogTitle>
          <DialogDescription>
            {created
              ? "Percona support has the database context already attached."
              : "Percona support reads this with the database context already attached."}
          </DialogDescription>
        </DialogHeader>

        {created ? (
          <TicketCreated
            ticketNumber={created.number}
            externalUrl={created.externalUrl}
            portalUrl={created.portalUrl ?? portalUrl}
            onClose={() => onOpenChange(false)}
          />
        ) : (
          <form
            className="space-y-4"
            noValidate
            onSubmit={(event) => {
              event.preventDefault();
              send.mutate({
                subject: draftSubject.trim(),
                description: draftDescription.trim(),
                context,
              });
            }}
          >
            <div className="space-y-1.5">
              <Label htmlFor={`${fieldId}-subject`}>Subject</Label>
              <Input
                id={`${fieldId}-subject`}
                value={draftSubject}
                onChange={(event) => setDraftSubject(event.target.value)}
                disabled={send.isPending}
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor={`${fieldId}-description`}>Description</Label>
              <textarea
                id={`${fieldId}-description`}
                rows={9}
                value={draftDescription}
                onChange={(event) => setDraftDescription(event.target.value)}
                disabled={send.isPending}
                className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-xs text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
              />
            </div>

            {connected ? (
              <p className="text-sm text-muted-foreground">
                Connection strings, passwords, and tokens are removed before the ticket is sent.
              </p>
            ) : (
              <NotConnected portalUrl={portalUrl} onLeave={() => onOpenChange(false)} />
            )}

            {send.error ? (
              <div
                role="alert"
                data-testid="support-send-error"
                className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
              >
                <CircleAlert
                  className="mt-0.5 size-4 shrink-0 text-destructive"
                  strokeWidth={1.5}
                  aria-hidden="true"
                />
                <span>{send.error.message} Your ticket is still here, so send it again.</span>
              </div>
            ) : null}

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={send.isPending}
                onClick={() => onOpenChange(false)}
              >
                Close
              </Button>
              {connected ? (
                <Button type="submit" disabled={!sendable} loading={send.isPending}>
                  {send.isPending ? "Sending..." : "Send ticket"}
                </Button>
              ) : null}
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/**
 * What a control plane with no ServiceNow connection shows instead of a send
 * button. It says where the connection is made and offers the route that works
 * today.
 */
function NotConnected({ portalUrl, onLeave }: { portalUrl: string; onLeave: () => void }) {
  return (
    <div className="space-y-2" data-testid="support-not-connected">
      <p className="text-sm">Support is not connected yet.</p>
      <p className="text-sm text-muted-foreground">
        Copy the text above into a case on the Percona support portal, or ask an admin to connect
        ServiceNow in Connectors.
      </p>
      <div className="flex flex-wrap items-center gap-4">
        <Link
          to="/connectors"
          onClick={onLeave}
          className="text-sm font-medium text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
        >
          Open Connectors
        </Link>
        <PortalLink href={portalUrl}>Open the support portal</PortalLink>
      </div>
    </div>
  );
}

type TicketCreatedProps = {
  ticketNumber: string | undefined;
  externalUrl: string | undefined;
  portalUrl: string;
  onClose: () => void;
};

/**
 * The success state. The number is what a developer quotes on a call, so it is
 * the largest thing on the panel and it is selectable mono text.
 */
function TicketCreated({ ticketNumber, externalUrl, portalUrl, onClose }: TicketCreatedProps) {
  const ticketURL = externalUrl ?? portalUrl;
  const linkLabel = externalUrl ? "Open the ticket in ServiceNow" : "Open the support portal";

  return (
    <div className="space-y-4" data-testid="support-ticket-created">
      <div className="flex items-start gap-3 rounded-lg border border-border px-4 py-4">
        <CircleCheck className="mt-0.5 size-5 text-success" strokeWidth={1.5} aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm font-medium">
            {ticketNumber ? (
              <>
                Ticket <span className="font-mono">{ticketNumber}</span> is open
              </>
            ) : (
              "Your ticket is open"
            )}
          </p>
          <p className="text-sm text-muted-foreground">
            {ticketNumber
              ? "Open the ticket to read what was sent. Quote this number if you call."
              : "Percona support has it. The number appears on the support page in a moment."}
          </p>
        </div>
      </div>

      {ticketNumber ? (
        <Button asChild>
          <Link to={`/support/${encodeURIComponent(ticketNumber)}`} onClick={onClose}>
            View ticket
          </Link>
        </Button>
      ) : null}

      <PortalLink href={ticketURL}>{linkLabel}</PortalLink>

      <DialogFooter>
        <Button type="button" onClick={onClose}>
          Close
        </Button>
      </DialogFooter>
    </div>
  );
}

function PortalLink({ href, children }: { href: string; children: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
    >
      {children}
      <ExternalLink className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
    </a>
  );
}
