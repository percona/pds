import {
  ChevronDown,
  ChevronRight,
  CircleCheck,
  CircleDashed,
  CircleX,
  Loader2,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ContactSupportButton, provisioningErrorMessage } from "@/features/support";
import { cn } from "@/lib/utils";
import { buildProvisioningSteps } from "../provisioning";
import type { ProvisioningEvents, ProvisioningStep, ProvisioningStepId } from "../provisioning";
import type { Database } from "../types";
import { assetUrl } from "@/lib/base-path";

type StepStyle = { label: string; icon: LucideIcon; className: string; spin?: boolean };

// Every step reads as icon plus text, so state never lands on color alone.
const stepStyles: Record<ProvisioningStep["state"], StepStyle> = {
  done: { label: "Done", icon: CircleCheck, className: "text-success" },
  active: { label: "In progress", icon: Loader2, className: "text-primary", spin: true },
  upcoming: { label: "Waiting", icon: CircleDashed, className: "text-muted-foreground" },
  failed: { label: "Failed", icon: CircleX, className: "text-destructive" },
};

type ProvisioningScreenProps = {
  database: Database;
  events?: ProvisioningEvents;
  failedStep?: ProvisioningStepId;
  /**
   * Whether the failure has held long enough to report. False keeps the create
   * on screen while the operator retries a reconcile.
   */
  failed?: boolean;
  onRetry: () => void;
  retrying?: boolean;
  /** Opens the delete dialog, which clears a failed create nothing was committed for. */
  onDelete?: () => void;
};

/**
 * The provisioning screen. It answers three questions: what is happening,
 * how long it usually takes, and what to do when it stops.
 */
export function ProvisioningScreen({
  database,
  events,
  failedStep,
  failed: failedOverride,
  onRetry,
  retrying,
  onDelete,
}: ProvisioningScreenProps) {
  const steps = buildProvisioningSteps(database, { events, failedStep, failed: failedOverride });
  const failed = steps.find((step) => step.state === "failed");

  return (
    <div className="mx-auto w-full max-w-md animate-fade-in space-y-6 py-8">
      <div className="flex flex-col items-center gap-3 text-center">
        {failed ? (
          <CircleX className="size-5 text-destructive" strokeWidth={1.5} aria-hidden="true" />
        ) : (
          <img src={assetUrl("percona-mark.svg")} alt="" className="size-6 animate-soft-pulse" />
        )}
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">
            {failed ? "Could not create " : "Creating "}
            <span className="font-mono">{database.name}</span>
          </h1>
          {failed ? null : <p className="text-sm text-muted-foreground">Usually 6 to 12 minutes</p>}
        </div>
      </div>

      <Card>
        <ul className="divide-y divide-border">
          {steps.map((step) => (
            <StepRow key={step.id} step={step} />
          ))}
        </ul>
      </Card>

      {failed ? (
        <div className="space-y-4">
          <div className="space-y-1">
            <p className="text-sm font-medium">{failed.label} failed.</p>
            <p className="text-sm text-muted-foreground">
              {database.message ?? "No reason was reported."}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button onClick={onRetry} loading={retrying}>
              Try again
            </Button>
            {/* The database's facts travel as the ticket context, with the
                failed step's last event as what went wrong. */}
            <ContactSupportButton
              subject={`Cannot create database ${database.name}`}
              database={database}
              errorMessage={provisioningErrorMessage(database, failed)}
            />
            {onDelete ? (
              <Button variant="outline" onClick={onDelete}>
                Delete
              </Button>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function StepRow({ step }: { step: ProvisioningStep }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const style = stepStyles[step.state];
  const Icon = style.icon;

  return (
    <li data-testid={`provisioning-step-${step.id}`}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen((current) => !current)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left"
      >
        <Icon
          className={cn("size-4 shrink-0", style.className, style.spin && "animate-spin")}
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <span className="flex-1 text-sm">{step.label}</span>
        <span className="text-xs text-muted-foreground">{style.label}</span>
        {open ? (
          <ChevronDown className="size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden />
        ) : (
          <ChevronRight className="size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden />
        )}
      </button>
      {open ? (
        <p id={panelId} className="px-4 pb-3 pl-11 font-mono text-xs text-muted-foreground">
          {step.event ?? "No event recorded yet."}
        </p>
      ) : null}
    </li>
  );
}
