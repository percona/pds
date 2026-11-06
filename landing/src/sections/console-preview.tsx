import {
  Bell,
  Bot,
  ChevronLeft,
  Database,
  LifeBuoy,
  Lock,
  MessageSquare,
  Plus,
  ScrollText,
  Server,
} from "lucide-react";
import { StatusPill, type StatusKind } from "@/components/status-pill";
import { Sparkline } from "@/components/sparkline";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// The sidebar a customer sees on the control plane: environments first, because
// every database belongs to one.
const nav = [
  { label: "Environments", icon: Server, active: true },
  { label: "Alerts", icon: Bell },
  { label: "Chats", icon: MessageSquare },
  { label: "AI agents", icon: Bot },
  { label: "Activity", icon: ScrollText },
  { label: "Support", icon: LifeBuoy },
];

const environmentName = "prod-us-west-2";

type Row = {
  name: string;
  engine: "MySQL" | "PostgreSQL";
  status: { kind: StatusKind; label: string };
  profile: string;
  lastBackup: string;
  cpu?: number[];
};

const rows: Row[] = [
  {
    name: "orders",
    engine: "MySQL",
    status: { kind: "ready", label: "Ready" },
    profile: "Prod",
    lastBackup: "2 hours ago",
    cpu: [22, 25, 24, 31, 28, 34, 30, 29, 36, 33, 31, 35],
  },
  {
    name: "payments",
    engine: "PostgreSQL",
    status: { kind: "ready", label: "Ready" },
    profile: "Prod",
    lastBackup: "40 minutes ago",
    cpu: [41, 38, 44, 40, 37, 39, 42, 38, 36, 35, 37, 34],
  },
  {
    name: "inventory-staging",
    engine: "MySQL",
    status: { kind: "ready", label: "Ready" },
    profile: "Staging",
    lastBackup: "6 hours ago",
    cpu: [12, 14, 13, 18, 16, 15, 21, 19, 17, 20, 18, 22],
  },
  {
    name: "search-dev",
    engine: "PostgreSQL",
    status: { kind: "creating", label: "Starting database" },
    profile: "Dev",
    lastBackup: "No backups yet",
  },
];

/**
 * A still of one environment's databases in the console, drawn in the
 * console's own tokens so it follows the theme. It is decoration: the figure
 * caption carries what it shows, and nothing inside is focusable.
 */
export function ConsolePreview() {
  return (
    <figure className="mx-auto max-w-5xl">
      <figcaption className="sr-only">
        One environment's databases in the PDS console: MySQL and PostgreSQL, with status, profile,
        last backup and CPU trend.
      </figcaption>
      <div
        aria-hidden="true"
        className="overflow-hidden rounded-lg border border-border bg-card text-left select-none"
      >
        <div className="flex h-10 items-center gap-2 border-b border-border px-4">
          <Lock className="size-3.5 text-muted-foreground" strokeWidth={1.5} />
          <span className="truncate font-mono text-xs text-muted-foreground">
            pds.percona.com/app/environments/{environmentName}
          </span>
        </div>

        <div className="grid md:grid-cols-[12rem_1fr]">
          <aside className="hidden border-r border-border bg-background p-3 md:block">
            <div className="flex items-center gap-2 px-2 py-2">
              <img src="/percona-mark.svg" alt="" className="size-4" />
              <span className="text-xs font-semibold tracking-tight">PDS</span>
            </div>
            <ul className="mt-2 space-y-0.5">
              {nav.map(({ label, icon: Icon, active }) => (
                <li
                  key={label}
                  className={cn(
                    "flex items-center gap-2 rounded-md px-2 py-1.5 text-xs",
                    active ? "bg-accent font-medium text-foreground" : "text-muted-foreground",
                  )}
                >
                  <Icon className="size-3.5" strokeWidth={1.5} />
                  {label}
                </li>
              ))}
            </ul>
          </aside>

          <div className="space-y-4 p-4 sm:p-6">
            <p className="flex items-center gap-1 text-xs text-muted-foreground">
              <ChevronLeft className="size-3.5" strokeWidth={1.5} />
              Back to environments
            </p>

            <div className="flex items-start justify-between gap-4">
              <div className="space-y-0.5">
                <p className="text-base font-semibold tracking-tight">Databases</p>
                <p className="text-xs text-muted-foreground">
                  MySQL or PostgreSQL on <span className="font-mono">{environmentName}</span>.
                </p>
              </div>
              <span className={buttonVariants({ size: "sm" })}>
                <Plus strokeWidth={1.5} />
                <span className="hidden sm:inline">Create database</span>
              </span>
            </div>

            <ul className="divide-y divide-border rounded-lg border border-border">
              {rows.map((row) => (
                <li
                  key={row.name}
                  className="grid grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_13rem_7rem] lg:grid-cols-[minmax(0,1fr)_13rem_7rem_8rem]"
                >
                  <div className="min-w-0 space-y-0.5">
                    <p className="truncate font-mono text-sm font-medium">{row.name}</p>
                    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                      <Database className="size-3" strokeWidth={1.5} />
                      {row.engine}
                    </span>
                  </div>
                  <div className="flex items-center gap-2">
                    <StatusPill kind={row.status.kind} label={row.status.label} />
                    <Badge variant="outline" className="hidden sm:inline-flex">
                      {row.profile}
                    </Badge>
                  </div>
                  <div className="hidden space-y-0.5 sm:block">
                    <p className="text-xs text-muted-foreground">Last backup</p>
                    <p className="text-xs">{row.lastBackup}</p>
                  </div>
                  <div className="hidden justify-end lg:flex">
                    {row.cpu ? (
                      <Sparkline points={row.cpu} />
                    ) : (
                      <StatusPill kind="pending" label="Metrics pending" />
                    )}
                  </div>
                </li>
              ))}
            </ul>
          </div>
        </div>
      </div>
    </figure>
  );
}
