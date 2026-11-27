import {
  CircleAlert,
  CircleArrowUp,
  CircleCheck,
  CircleX,
  LoaderCircle,
  TriangleAlert,
  Unplug,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { Alert } from "@/features/alerts";
import { accountName, type AwsAccount } from "@/features/aws-accounts";
import { databaseHref, type Database } from "@/features/databases";
import { lifecycleNotes, type Environment } from "@/features/environments";
import { formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { environmentPhase, inSentence } from "../environment-phase";

type Tone = "critical" | "warning" | "muted" | "primary";

type Item = {
  key: string;
  icon: LucideIcon;
  tone: Tone;
  title: ReactNode;
  body: ReactNode;
  to: string;
  label: string;
};

const toneClass: Record<Tone, string> = {
  critical: "text-destructive",
  warning: "text-warning",
  muted: "text-muted-foreground",
  primary: "text-primary",
};

type NeedsAttentionProps = {
  environments: Environment[];
  databases: Database[];
  accounts: AwsAccount[];
  alerts: Alert[];
};

/** Everything that wants a person: firing alerts, quiet environments, and managed ones in trouble. */
export function NeedsAttention({ environments, databases, accounts, alerts }: NeedsAttentionProps) {
  const items = [
    ...alertItems(alerts, databases),
    ...environments.flatMap((environment) =>
      environment.origin === "managed"
        ? managedItems(environment, accounts)
        : joinedItems(environment),
    ),
  ];

  return (
    <Card className="overflow-hidden" aria-labelledby="attention-heading" role="region">
      <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-4">
        <h2 id="attention-heading" className="text-base font-semibold">
          Needs attention
        </h2>
        <p className="text-xs text-muted-foreground">
          <span className="font-mono">{items.length}</span> {items.length === 1 ? "item" : "items"}
        </p>
      </div>
      {items.length === 0 ? (
        <div className="flex flex-col items-center gap-2 px-5 py-10 text-center">
          <CircleCheck className="size-5 text-success" strokeWidth={1.5} aria-hidden="true" />
          <p className="text-sm text-muted-foreground">Nothing needs you right now.</p>
        </div>
      ) : (
        <ul className="divide-y divide-border">
          {items.map((item) => (
            <li
              key={item.key}
              className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-x-3 gap-y-2 px-5 py-3 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center"
            >
              <item.icon
                className={cn("mt-0.5 size-4 sm:mt-0", toneClass[item.tone])}
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <div className="min-w-0 space-y-0.5">
                <p className="text-sm font-medium break-words">{item.title}</p>
                <p className="text-xs text-muted-foreground">{item.body}</p>
              </div>
              <Button
                asChild
                variant="ghost"
                size="sm"
                className="col-start-2 justify-self-start sm:col-start-3"
              >
                <Link to={item.to}>{item.label}</Link>
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function environmentPath(name: string): string {
  return `/environments/${encodeURIComponent(name)}`;
}

/** The name a database id carries after its namespace, for an alert whose database is not listed. */
function nameFromId(id: string): string {
  const resource = id.split("/").pop() ?? id;
  const dot = resource.indexOf(".");

  return dot === -1 ? resource : resource.slice(dot + 1);
}

function alertItems(alerts: Alert[], databases: Database[]): Item[] {
  return alerts
    .filter((alert) => alert.severity === "critical" || alert.severity === "warning")
    .map((alert) => {
      const since = formatRelativeTime(alert.startsAt);
      const database = alert.databaseId
        ? (databases.find((candidate) => candidate.id === alert.databaseId)?.name ??
          nameFromId(alert.databaseId))
        : undefined;

      return {
        key: `alert-${alert.id}`,
        icon: alert.severity === "critical" ? CircleAlert : TriangleAlert,
        tone: alert.severity === "critical" ? "critical" : "warning",
        title: (
          <>
            <span className="sr-only">
              {alert.severity === "critical" ? "Critical: " : "Warning: "}
            </span>
            {alert.summary}
          </>
        ),
        body: (
          <>
            {database ? (
              <>
                <span className="font-mono">{database}</span> on{" "}
              </>
            ) : null}
            <span className="font-mono">{alert.environment}</span>
            {since ? `, started ${inSentence(since)}` : ""}
          </>
        ),
        to: alert.databaseId ? databaseHref(alert.databaseId) : environmentPath(alert.environment),
        label: alert.databaseId ? "Open database" : "Open environment",
      };
    });
}

function joinedItems(environment: Environment): Item[] {
  const phase = environmentPhase(environment);
  const to = environmentPath(environment.name);

  if (phase === "unreachable") {
    const lastSeen = formatRelativeTime(environment.lastSeen);

    return [
      {
        key: `quiet-${environment.name}`,
        icon: Unplug,
        tone: "warning",
        title: <Named name={environment.name} rest=" stopped reporting" />,
        body: `${lastSeen ? `Last seen ${inSentence(lastSeen)}. ` : ""}Changes you make are applied when it reconnects.`,
        to,
        label: "Open environment",
      },
    ];
  }
  if (phase === "joining") {
    return [
      {
        key: `joining-${environment.name}`,
        icon: LoaderCircle,
        tone: "muted",
        title: <Named name={environment.name} rest=" has not reported yet" />,
        body: "The command that connects it may still be running.",
        to,
        label: "Open environment",
      },
    ];
  }

  return [];
}

function managedItems(environment: Environment, accounts: AwsAccount[]): Item[] {
  const to = environmentPath(environment.name);
  const name = environment.name;

  if (environment.state === "failed") {
    return [
      {
        key: `failed-${name}`,
        icon: CircleX,
        tone: "critical",
        title: <Named name={name} rest=" could not be created" />,
        body:
          environment.provisioning?.failure?.sentence ??
          "A step stopped. Open the environment to see what AWS refused.",
        to,
        label: "Open environment",
      },
    ];
  }
  if (environment.state === "lost") {
    return [
      {
        key: `lost-${name}`,
        icon: CircleX,
        tone: "critical",
        title: <Named name={name} rest=" is lost" />,
        body:
          environment.lifecycle?.lostReason ??
          "PDS found no network or Kubernetes cluster for it in AWS. Its databases are not running.",
        to,
        label: "Open environment",
      },
    ];
  }
  if (environment.state === "creating" || environment.state === "deleting") {
    return [];
  }

  const items: Item[] = [];
  const account = accounts.find((candidate) => candidate.id === environment.connectionId);
  if (account?.state === "unreachable") {
    items.push({
      key: `account-${name}`,
      icon: Unplug,
      tone: "warning",
      title: (
        <>
          PDS cannot reach AWS account <span className="font-mono">{accountName(account)}</span>
        </>
      ),
      body: account.reason ?? (
        <>
          It runs <span className="font-mono">{name}</span>. Run the connect command again to repair
          it.
        </>
      ),
      to: `/environments?fix=${encodeURIComponent(account.id)}#aws-accounts`,
      label: "Fix the AWS account",
    });
  }
  for (const note of lifecycleNotes(environment.lifecycle)) {
    items.push(
      note.kind === "update"
        ? {
            key: `update-${name}`,
            icon: CircleArrowUp,
            tone: "primary",
            title: <Named name={name} rest=" has an update available" />,
            body: "Review what changes and when it runs, on the Updates tab.",
            to,
            label: "Open environment",
          }
        : {
            key: `drift-${name}`,
            icon: TriangleAlert,
            tone: "warning",
            title: <Named name={name} rest=" changed outside PDS" />,
            body: "PDS reports it and does not change it.",
            to,
            label: "Open environment",
          },
    );
  }

  return items;
}

function Named({ name, rest }: { name: string; rest: string }) {
  return (
    <>
      <span className="font-mono">{name}</span>
      {rest}
    </>
  );
}
