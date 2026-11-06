import { Ban, CircleCheck, CircleX, ClipboardCheck, Clock, type LucideIcon } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { InlineError } from "@/components/ui/inline-error";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { ApprovalDecision } from "../api";
import type { ChatApproval, LiveApproval } from "../types";

const ended: Record<
  Exclude<LiveApproval["status"], "open">,
  { icon: LucideIcon; text: string; tone: string }
> = {
  approved: {
    icon: CircleCheck,
    text: "Approved. The change is being made.",
    tone: "text-success",
  },
  rejected: { icon: CircleX, text: "Rejected. Nothing changed.", tone: "text-muted-foreground" },
  expired: {
    icon: Clock,
    text: "This approval expired. Nothing changed.",
    tone: "text-warning",
  },
  cancelled: { icon: Ban, text: "Cancelled. Nothing changed.", tone: "text-muted-foreground" },
};

function EndedNote({ outcome }: { outcome: keyof typeof ended }) {
  const { icon: Icon, text, tone } = ended[outcome];

  return (
    <p className={`flex items-center gap-2 text-sm ${tone}`}>
      <Icon className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
      {text}
    </p>
  );
}

/** Whether the approval's time has run out, checked each second while it is open. */
function useLapsed(expiresAt: string, watching: boolean): boolean {
  const [lapsed, setLapsed] = useState(() => Date.parse(expiresAt) <= Date.now());

  useEffect(() => {
    if (!watching || lapsed) {
      return;
    }
    const timer = setInterval(() => {
      if (Date.parse(expiresAt) <= Date.now()) {
        setLapsed(true);
      }
    }, 1000);

    return () => clearInterval(timer);
  }, [expiresAt, watching, lapsed]);

  return lapsed;
}

function expiryTime(approval: ChatApproval): string {
  return new Date(approval.expiresAt).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * A change the model asked for, put to the person with every argument it will
 * be made with. Nothing happens until they approve it. A delete also asks for
 * the database's name, typed.
 */
export function ApprovalCard({
  approval,
  status,
  onDecide,
}: {
  approval: ChatApproval;
  status: LiveApproval["status"];
  onDecide: (decision: ApprovalDecision) => Promise<unknown>;
}) {
  const nameId = useId();
  const [typed, setTyped] = useState("");
  const [sending, setSending] = useState(false);
  const [refused, setRefused] = useState<string | undefined>(undefined);
  const lapsed = useLapsed(approval.expiresAt, status === "open");
  const outcome = status === "open" && lapsed ? "expired" : status;

  const decide = async (decision: ApprovalDecision) => {
    setSending(true);
    setRefused(undefined);
    try {
      await onDecide(decision);
    } catch (error) {
      setRefused(error instanceof Error ? error.message : "The answer could not be sent.");
    } finally {
      setSending(false);
    }
  };

  const nameMatches = !approval.destructive || typed === approval.database;

  return (
    <section
      aria-label={approval.title}
      className="max-w-[85%] space-y-3 rounded-lg border border-border bg-card p-3"
    >
      <h3 className="flex items-center gap-2 text-base font-semibold">
        <ClipboardCheck className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        {approval.title}
      </h3>
      <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
        {approval.lines.map((line, index) => (
          <div key={`${line.label}-${index}`} className="contents">
            <dt className="text-muted-foreground">{line.label}</dt>
            <dd className="font-mono break-words whitespace-pre-wrap">{line.value}</dd>
          </div>
        ))}
      </dl>

      {outcome === "open" ? (
        <>
          {approval.destructive && approval.database ? (
            <div className="space-y-1">
              <Label htmlFor={nameId}>{`Type ${approval.database} to confirm`}</Label>
              <Input
                id={nameId}
                value={typed}
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                onChange={(event) => setTyped(event.target.value)}
              />
            </div>
          ) : null}
          <InlineError message={refused} />
          <div className="flex items-center gap-2">
            <Button
              type="button"
              size="sm"
              variant={approval.destructive ? "destructive" : "default"}
              disabled={!nameMatches || sending}
              onClick={() =>
                void decide(
                  approval.destructive ? { approve: true, name: typed } : { approve: true },
                )
              }
            >
              Approve
            </Button>
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={sending}
              onClick={() => void decide({ approve: false })}
            >
              Reject
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            Nothing changes until you approve. Expires at{" "}
            <span className="font-mono">{expiryTime(approval)}</span>.
          </p>
        </>
      ) : (
        <EndedNote outcome={outcome} />
      )}
    </section>
  );
}
