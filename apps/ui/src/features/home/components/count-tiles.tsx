import { Bell, Database as DatabaseIcon, Server, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Card } from "@/components/ui/card";
import type { AlertReport } from "@/features/alerts";
import { databaseStatus, type Database } from "@/features/databases";
import type { Environment } from "@/features/environments";
import { cn } from "@/lib/utils";
import { environmentPhase, type EnvironmentPhase } from "../environment-phase";

type Tone = "success" | "warning" | "critical" | "muted";

/** One part of a count, such as "2 connected". The dot repeats what the words say. */
type Part = { key: string; count: number; label: string; tone: Tone };

const dotTone: Record<Tone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  critical: "bg-destructive",
  muted: "bg-muted-foreground",
};

const environmentParts: { phase: EnvironmentPhase; label: string; tone: Tone }[] = [
  { phase: "connected", label: "connected", tone: "success" },
  { phase: "unreachable", label: "unreachable", tone: "warning" },
  { phase: "joining", label: "joining", tone: "muted" },
  { phase: "creating", label: "being created", tone: "muted" },
  { phase: "failed", label: "failed", tone: "critical" },
  { phase: "deleting", label: "being deleted", tone: "muted" },
];

const databaseParts: { kind: string; label: string; tone: Tone }[] = [
  { kind: "ready", label: "ready", tone: "success" },
  { kind: "creating", label: "in progress", tone: "muted" },
  { kind: "degraded", label: "degraded", tone: "warning" },
  { kind: "failed", label: "failed", tone: "critical" },
];

type CountTilesProps = {
  environments: Environment[];
  databases: Database[];
  /** Absent while alerts are being read or could not be read. */
  alerts?: AlertReport;
};

/** How many environments, databases and firing alerts there are, and how they stand. */
export function CountTiles({ environments, databases, alerts }: CountTilesProps) {
  const phases = environments.map(environmentPhase);
  const kinds = databases.map((database) => databaseStatus(database).kind);
  const firing = alerts?.alerts ?? [];
  const severities: Part[] = [
    {
      key: "critical",
      count: firing.filter((alert) => alert.severity === "critical").length,
      label: "critical",
      tone: "critical",
    },
    {
      key: "warning",
      count: firing.filter((alert) => alert.severity === "warning").length,
      label: "warning",
      tone: "warning",
    },
    {
      key: "info",
      count: firing.filter((alert) => alert.severity === "info").length,
      label: "info",
      tone: "muted",
    },
  ];

  return (
    <Card className="grid overflow-hidden sm:grid-cols-3">
      <Tile
        to="/environments"
        label="Environments"
        icon={Server}
        value={environments.length}
        parts={environmentParts.map(({ phase, label, tone }) => ({
          key: phase,
          count: phases.filter((candidate) => candidate === phase).length,
          label,
          tone,
        }))}
      />
      <Tile
        label="Databases"
        icon={DatabaseIcon}
        value={databases.length}
        parts={databaseParts.map(({ kind, label, tone }) => ({
          key: kind,
          count: kinds.filter((candidate) => candidate === kind).length,
          label,
          tone,
        }))}
      />
      <Tile
        to="/alerts"
        label="Alerts firing"
        icon={Bell}
        value={alerts ? firing.length : undefined}
        parts={severities}
        none={
          alerts ? (
            <PartLabel tone="success">Nothing firing</PartLabel>
          ) : (
            <span className="text-muted-foreground">Not available</span>
          )
        }
      />
    </Card>
  );
}

function Tile({
  to,
  label,
  icon: Icon,
  value,
  parts,
  none,
}: {
  to?: string;
  label: string;
  icon: LucideIcon;
  /** Undefined when there is no number to show, which is not zero. */
  value: number | undefined;
  parts: Part[];
  none?: ReactNode;
}) {
  const shown = parts.filter((part) => part.count > 0);
  const body = (
    <>
      <span className="flex items-center justify-between text-sm text-muted-foreground">
        {label}
        <Icon className="size-4" strokeWidth={1.5} aria-hidden="true" />
      </span>
      <span className="font-mono text-2xl font-semibold tracking-tight">{value ?? "-"}</span>
      <span className="flex flex-wrap gap-x-3 gap-y-1 text-xs">
        {shown.length > 0
          ? shown.map((part) => (
              <PartLabel key={part.key} tone={part.tone}>
                <span className="font-mono">{part.count}</span> {part.label}
              </PartLabel>
            ))
          : none}
      </span>
    </>
  );
  const className =
    "flex flex-col gap-2 border-border px-5 py-4 not-first:border-t sm:not-first:border-t-0 sm:not-first:border-l";

  return to ? (
    <Link
      to={to}
      className={cn(
        className,
        "hover:bg-accent focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary",
      )}
    >
      {body}
    </Link>
  ) : (
    <div className={className}>{body}</div>
  );
}

function PartLabel({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("size-2 rounded-full", dotTone[tone])} aria-hidden="true" />
      <span>{children}</span>
    </span>
  );
}
