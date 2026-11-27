import { MessageSquare } from "lucide-react";
import { useRef, type ReactNode } from "react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useChatUi } from "@/features/chat";
import { databaseHref } from "@/features/databases";
import { useFeature } from "@/features/flags";
import { ContactSupportButton } from "@/features/support";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useNow } from "@/lib/use-now";
import { alertActionSummary, alertTarget } from "../actions";
import { useEnvironmentTeam } from "../use-environment-team";
import type { Alert } from "../types";
import { AlertSeverityBadge } from "./alert-severity";

type AlertPanelProps = {
  /** The alert to show. The panel is closed while this is undefined. */
  alert: Alert | undefined;
  /** When monitoring was last read for the alert's environment. */
  readAt?: string | null;
  /**
   * The control that opened the panel. The dialog has no trigger of its own, so
   * focus is handed back to this when it closes.
   */
  opener?: HTMLElement | null;
  onClose: () => void;
};

/**
 * One alert in a panel beside the list: where it fires, the labels monitoring
 * sent with it, and the three ways to act on it. It is a modal dialog, so focus
 * stays inside until it closes and then returns to the row that opened it.
 */
export function AlertPanel({ alert, readAt, opener, onClose }: AlertPanelProps) {
  // Ask PDS hands focus to the chat it opens, so the row must not take it back.
  const toChat = useRef(false);

  return (
    <Dialog open={alert !== undefined} onOpenChange={(open) => (open ? undefined : onClose())}>
      <DialogContent
        className="top-2 right-2 bottom-2 left-2 w-auto max-w-none translate-x-0 translate-y-0 overflow-y-auto sm:left-auto sm:w-full sm:max-w-md"
        data-testid="alert-panel"
        onOpenAutoFocus={() => {
          toChat.current = false;
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          if (!toChat.current && opener?.isConnected) {
            opener.focus();
          }
        }}
      >
        {alert ? (
          <AlertPanelBody
            alert={alert}
            readAt={readAt}
            onClose={onClose}
            onAsk={() => {
              toChat.current = true;
            }}
          />
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

type AlertPanelBodyProps = {
  alert: Alert;
  readAt?: string | null;
  onClose: () => void;
  onAsk: () => void;
};

function AlertPanelBody({ alert, readAt, onClose, onAsk }: AlertPanelBodyProps) {
  const team = useEnvironmentTeam(alert.environment);
  const { to, state } = alertTarget(alert, team);
  const now = useNow();
  const chatOn = useFeature("chat");
  const { ask } = useChatUi();
  const firing = formatRelativeTime(alert.startsAt, now);
  const read = formatRelativeTime(readAt ?? undefined, now);
  const labels = Object.entries(alert.labels ?? {});
  const about = alert.databaseId ?? alert.environment;

  return (
    <div className="space-y-6">
      <DialogHeader className="pr-8 pb-0">
        <DialogTitle>{alert.summary}</DialogTitle>
        <DialogDescription asChild>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <AlertSeverityBadge severity={alert.severity} />
            <span className="text-xs" title={formatTimestamp(alert.startsAt) ?? undefined}>
              Firing since {firing ?? "an unknown time"}
            </span>
          </div>
        </DialogDescription>
      </DialogHeader>

      <Section title="Where">
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
          {alert.databaseId ? (
            <Fact label="Database">
              <Link
                to={databaseHref(alert.databaseId)}
                onClick={onClose}
                className="font-mono text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
              >
                {alert.databaseId}
              </Link>
            </Fact>
          ) : null}
          <Fact label="Environment">
            <Link
              to={`/environments/${encodeURIComponent(alert.environment)}`}
              onClick={onClose}
              className="font-mono text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
            >
              {alert.environment}
            </Link>
          </Fact>
          <Fact label="Rule">
            <span className="font-mono">{alert.rule}</span>
          </Fact>
          <Fact label="Monitoring read">
            <span title={formatTimestamp(readAt ?? undefined) ?? undefined}>
              {read ?? "Not read yet"}
            </span>
          </Fact>
        </dl>
      </Section>

      <Section title="Labels from monitoring">
        {labels.length > 0 ? (
          <dl
            className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 font-mono text-xs"
            data-testid="alert-labels"
          >
            {labels.map(([key, value]) => (
              <Fact key={key} label={key}>
                <span className="break-all">{value}</span>
              </Fact>
            ))}
          </dl>
        ) : (
          <p className="text-sm text-muted-foreground">
            Monitoring sent no labels with this alert.
          </p>
        )}
      </Section>

      <Section title="What you can do">
        <div className="space-y-4">
          <Action note={alertActionSummary(alert)}>
            <Button asChild>
              <Link to={to} state={state} onClick={onClose}>
                {alert.action.label}
              </Link>
            </Button>
          </Action>
          {chatOn ? (
            <Action
              note={
                <>
                  Starts a chat about <span className="font-mono">{about}</span>. Nothing is sent
                  until you send it.
                </>
              }
            >
              <Button
                variant="outline"
                onClick={() => {
                  onAsk();
                  ask(`Why is ${about} showing "${alert.summary}"?`, {
                    databaseId: alert.databaseId,
                  });
                  onClose();
                }}
              >
                <MessageSquare strokeWidth={1.5} aria-hidden="true" />
                Ask PDS about this alert
              </Button>
            </Action>
          ) : null}
          <Action note="Opens a case with the alert attached.">
            <ContactSupportButton alert={alert} />
          </Action>
        </div>
        <p className="text-xs text-muted-foreground">
          An alert clears by itself when monitoring stops seeing the problem.
        </p>
      </Section>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-3">
      <h3 className="text-sm font-semibold">{title}</h3>
      {children}
    </section>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

function Action({ note, children }: { note: ReactNode; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div>{children}</div>
      <p className="text-xs text-muted-foreground">{note}</p>
    </div>
  );
}
