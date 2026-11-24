import { Copy, Lock, Plug } from "lucide-react";
import type { CSSProperties, ReactNode } from "react";
import { Button, Stage, Window } from "@/vendor/stageshow";
import { usePageTheme } from "@/lib/page-theme";
import { engineNames, environmentName } from "@/lib/configurator";
import { Pill } from "./faux-console";

export type OperationId = "scale" | "backups" | "connect" | "alerts" | "agents" | "support";

const databaseUrl = `databases/${environmentName}/pds.orders`;

function Frame({
  label,
  height,
  children,
}: {
  label: string;
  height: number;
  children: ReactNode;
}) {
  const theme = usePageTheme();

  return (
    <figure className="overflow-hidden rounded-lg border border-border bg-muted/40">
      <figcaption className="sr-only">{label}</figcaption>
      <div aria-hidden="true" inert className="overflow-hidden">
        <Stage
          width={520}
          height={height}
          compact={{ below: 440, width: 358, height: Math.min(height, 330) }}
          theme={theme}
          className="fx pds-stage"
        >
          {() => children}
        </Stage>
      </div>
    </figure>
  );
}

function Screen({ url, scrim, children }: { url: string; scrim?: boolean; children: ReactNode }) {
  return (
    <Window
      className="fx-window fx-still-window"
      style={{ top: 16, left: 16 } as CSSProperties}
      url={<span className="fx-url">{`pds.percona.com/app/${url}`}</span>}
      body={{ className: `fx-body${scrim ? " fx-scrim" : ""}` }}
    >
      {children}
    </Window>
  );
}

function Header({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="fx-head">
      <div>
        <p className="fx-title">{title}</p>
        {description ? <p className="fx-small">{description}</p> : null}
      </div>
      {action}
    </div>
  );
}

function Field({ label, value, mono = true }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="fx-field">
      <span className="fx-label">{label}</span>
      <span className="fx-input" data-text={mono ? undefined : ""}>
        {value}
      </span>
    </div>
  );
}

const backups = [
  { started: "2 hours ago", size: "1.2 GB", type: "Scheduled" },
  { started: "1 day ago", size: "1.1 GB", type: "Scheduled" },
  { started: "3 days ago", size: "980 MB", type: "On demand" },
];

const alerts = [
  {
    summary: "High CPU on orders",
    kind: "warning" as const,
    label: "Warning",
    since: "Firing since 2 hours ago",
    read: "Read 1 minute ago",
  },
  {
    summary: "Backup failed on payments",
    kind: "critical" as const,
    label: "Critical",
    since: "Firing since 40 minutes ago",
    read: "Read 1 minute ago",
  },
];

// Each still is a screen of the production console. Dialogs sit on the page's scrim.
const stills: Record<OperationId, { label: string; height: number; render: () => ReactNode }> = {
  scale: {
    label: "The Scale dialog for orders with CPU cores, memory and members, and a Scale button.",
    height: 404,
    render: () => (
      <Screen url={databaseUrl} scrim>
        <div className="fx-dialog">
          <div className="fx-dialog-head">
            <p className="fx-title" style={{ fontSize: 15 }}>
              Scale orders
            </p>
            <p className="fx-small">
              Members restart one at a time to apply this. Storage can only grow.
            </p>
          </div>
          <div className="fx-two">
            <Field label="CPU cores" value="4" />
            <Field label="Memory in GB" value="16" />
          </div>
          <div className="fx-field fx-wide-only">
            <span className="fx-label">Members</span>
            <div className="fx-choices">
              <span className="fx-choice">1</span>
              <span className="fx-choice" data-on="">
                3
              </span>
            </div>
          </div>
          <div className="fx-footer">
            <Button>
              <span>Cancel</span>
            </Button>
            <Button variant="primary">
              <span>Scale</span>
            </Button>
          </div>
        </div>
      </Screen>
    ),
  },
  backups: {
    label:
      "The backups list for orders with Started, Size, Type and Status columns and a Restore button on each row.",
    height: 322,
    render: () => (
      <Screen url={databaseUrl}>
        <Header
          title="Backups"
          action={
            <Button variant="primary" size="small">
              <span>Back up now</span>
            </Button>
          }
        />
        <div className="fx-card">
          <div className="fx-tr fx-th">
            <span>Started</span>
            <span>Size</span>
            <span>Type</span>
            <span>Status</span>
            <span style={{ width: 60 }} />
          </div>
          {backups.map((backup) => (
            <div key={backup.started} className="fx-tr">
              <span className="fx-strong">{backup.started}</span>
              <span className="fx-mono">{backup.size}</span>
              <span>{backup.type}</span>
              <span>
                <Pill kind="ready">Succeeded</Pill>
              </span>
              <Button size="small">
                <span>Restore</span>
              </Button>
            </div>
          ))}
        </div>
      </Screen>
    ),
  },
  connect: {
    label: "The Connect card for orders: host, port, user and a masked connection string.",
    height: 334,
    render: () => (
      <Screen url={databaseUrl}>
        <div className="fx-card" style={{ padding: 16, gap: 14 }}>
          <p className="fx-title" style={{ fontSize: 14 }}>
            Connect
          </p>
          <p className="fx-line">
            <Lock className="fx-icon" size={14} strokeWidth={1.5} />
            Reachable inside the Kubernetes cluster only
          </p>
          <div className="fx-dl">
            {[
              ["Host", "orders-haproxy.pds"],
              ["Port", "3306"],
              ["User", "app"],
            ].map(([label, value]) => (
              <div key={label}>
                <span className="fx-small">{label}</span>
                <span className="fx-mono">{value}</span>
              </div>
            ))}
          </div>
          <div className="fx-stack">
            <span className="fx-small">Connection string</span>
            <p className="fx-pre">mysql://app:••••••••@orders-haproxy.pds:3306</p>
          </div>
          <div>
            <Button icon={<Plug size={12} strokeWidth={1.5} />}>
              <span>Connect</span>
            </Button>
          </div>
        </div>
      </Screen>
    ),
  },
  alerts: {
    label:
      "The Alerts page with a warning and a critical alert firing, each with its environment and time.",
    height: 326,
    render: () => (
      <Screen url="alerts">
        <Header
          title="Alerts"
          description="What is firing in your databases, across every environment."
        />
        <div className="fx-card">
          {alerts.map((alert) => (
            <div key={alert.summary} className="fx-row" style={{ alignItems: "flex-start" }}>
              <div className="fx-stack" style={{ gap: 7 }}>
                <p className="fx-strong" style={{ fontSize: 12, fontWeight: 500 }}>
                  {alert.summary}
                </p>
                <div className="fx-alert-meta">
                  <Pill kind={alert.kind}>{alert.label}</Pill>
                  <span className="fx-mono">{environmentName}</span>
                  <span>{alert.since}</span>
                  <span>{alert.read}</span>
                </div>
              </div>
            </div>
          ))}
        </div>
      </Screen>
    ),
  },
  agents: {
    label: "The AI agents page, step 1: the Claude Code plugin install commands.",
    height: 318,
    render: () => (
      <Screen url="agents">
        <Header title="AI agents" />
        <div style={{ display: "flex", gap: 12 }}>
          <span className="fx-step-num">1</span>
          <div className="fx-stack" style={{ gap: 8, flex: 1 }}>
            <p className="fx-title" style={{ fontSize: 14 }}>
              Claude Code plugin
            </p>
            <p className="fx-strong" style={{ fontSize: 12 }}>
              Run this in a terminal. The plugin adds PDS and a local data server for{" "}
              {engineNames("and")}.
            </p>
            <p className="fx-pre">
              {"claude plugin marketplace add percona/pds\nclaude plugin install pds@percona-pds"}
            </p>
            <div>
              <Button icon={<Copy size={12} strokeWidth={1.5} />}>
                <span>Copy install commands</span>
              </Button>
            </div>
          </div>
        </div>
      </Screen>
    ),
  },
  support: {
    label: "The Contact support dialog with a subject, a description and a Send ticket button.",
    height: 404,
    render: () => (
      <Screen url={databaseUrl} scrim>
        <div className="fx-dialog">
          <div className="fx-dialog-head">
            <p className="fx-title" style={{ fontSize: 15 }}>
              Contact support
            </p>
            <p className="fx-small">
              Percona support reads this with the database context already attached.
            </p>
          </div>
          <Field label="Subject" value="Cannot connect to orders" mono={false} />
          <div className="fx-field">
            <span className="fx-label">Description</span>
            <p className="fx-area">{"Database: orders\nEngine: mysql\nProfile: prod"}</p>
          </div>
          <div className="fx-footer">
            <Button>
              <span>Close</span>
            </Button>
            <Button variant="primary">
              <span>Send ticket</span>
            </Button>
          </div>
        </div>
      </Screen>
    ),
  },
};

/** A small still of the console for one operation. */
export function OperationStill({ id }: { id: OperationId }) {
  const still = stills[id];

  return (
    <Frame label={still.label} height={still.height}>
      {still.render()}
    </Frame>
  );
}
