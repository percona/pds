import { Check } from "lucide-react";
import { useId, useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { ConnectedAgents } from "@/features/agent-sessions";
import {
  accessChoices,
  bridgeConfig,
  claudeCodeInstall,
  claudeCodeStart,
  claudeCodeUpdate,
  defaultOrigin,
  loginCommand,
  mcpAddress,
  type Access,
} from "@/features/agents";
import { CommandBlock } from "@/features/environments";
import { AiSettingsSummary } from "@/features/organization-ai";
import { cn } from "@/lib/utils";

const intro =
  "Let an AI agent in your editor or chat client list your databases, read metrics, alerts and logs, and, if you allow it, change them. The agent signs in as you, with no password in your client's settings. What it does is recorded as yours.";

function Step({
  number,
  title,
  children,
}: {
  number: number;
  title: string;
  children: React.ReactNode;
}) {
  const titleId = useId();

  return (
    <li>
      <section aria-labelledby={titleId} className="flex gap-4">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-full border border-border font-mono text-xs">
          {number}
        </span>
        <div className="min-w-0 flex-1 space-y-2">
          <h2 id={titleId} className="text-base font-semibold">
            {title}
          </h2>
          {children}
        </div>
      </section>
    </li>
  );
}

function Address({ origin }: { origin: string }) {
  return <CommandBlock command={mcpAddress(origin)} label="Copy address" copied="Address copied" />;
}

export function AgentsPage() {
  const [access, setAccess] = useState<Access>("read");
  const origin = window.location.origin;
  const selfHosted = origin !== defaultOrigin;

  return (
    <div className="max-w-3xl animate-fade-in space-y-6">
      <PageHeader title="AI agents" description={intro} />
      <AiSettingsSummary />
      <ol className="space-y-6">
        <Step number={1} title="Claude Code plugin">
          <p className="text-sm">
            Run this in a terminal. The plugin adds PDS and a local data server for MySQL and
            PostgreSQL.
          </p>
          <CommandBlock
            command={claudeCodeInstall}
            label="Copy install commands"
            copied="Install commands copied"
          />
          {selfHosted ? (
            <>
              <p className="text-sm">
                The plugin signs in to {defaultOrigin} unless you start Claude Code with this
                control plane&apos;s address.
              </p>
              <CommandBlock
                command={claudeCodeStart(origin)}
                label="Copy start command"
                copied="Start command copied"
              />
            </>
          ) : null}
          <p className="text-sm text-muted-foreground">
            Open /mcp in Claude Code, choose plugin:pds:pds and sign in in your browser. To update,
            run these and restart.
          </p>
          <CommandBlock
            command={claudeCodeUpdate}
            label="Copy update commands"
            copied="Update commands copied"
          />
        </Step>

        <Step number={2} title="Cursor plugin">
          <p className="text-sm">
            In Cursor, open Customize and add the marketplace{" "}
            <span className="font-mono">percona-pds</span> from the repository{" "}
            <span className="font-mono">percona/pds</span>. Then install{" "}
            <span className="font-mono">pds</span>.
          </p>
          {selfHosted ? (
            <>
              <p className="text-sm">
                The plugin points at {defaultOrigin}. Add this control plane as a remote MCP server
                by hand with this address, and sign in in your browser.
              </p>
              <Address origin={origin} />
            </>
          ) : null}
        </Step>

        <Step number={3} title="Other clients with remote servers">
          <p className="text-sm">
            Add this address as a remote MCP server in Claude Desktop (Settings, Connectors, Add
            custom connector), VS Code, or any client that supports them. The client opens your
            browser to sign in. Check what the agent may do, then allow it.
          </p>
          <Address origin={origin} />
        </Step>

        <Step number={4} title="Clients that only run a local command">
          <p className="text-sm">Choose what the agent may do when you sign it in.</p>
          <div className="grid gap-2 sm:grid-cols-3">
            {accessChoices.map((choice) => {
              const selected = access === choice.id;

              return (
                <button
                  key={choice.id}
                  type="button"
                  aria-pressed={selected}
                  onClick={() => setAccess(choice.id)}
                  className={cn(
                    "rounded-lg border p-3 text-left text-sm",
                    selected ? "border-primary bg-accent" : "border-border",
                  )}
                >
                  <span className="flex items-center gap-1 font-medium">
                    {selected ? (
                      <Check
                        data-testid="access-check"
                        className="size-4"
                        strokeWidth={1.5}
                        aria-hidden="true"
                      />
                    ) : null}
                    {choice.title}
                  </span>
                  <span className="text-muted-foreground">{choice.hint}</span>
                </button>
              );
            })}
          </div>
          <p className="text-sm">
            Run this once in a terminal on the machine where the agent runs.
          </p>
          <CommandBlock
            command={loginCommand(origin, access)}
            label="Copy login command"
            copied="Login command copied"
          />
          <p className="text-sm text-muted-foreground">
            It opens your browser. Allow access and the agent works as you. Run it again if the sign
            in expires or you want to allow more.
          </p>
          <p className="text-sm">
            Then add this to your client&apos;s MCP settings and restart it.
          </p>
          <CommandBlock command={bridgeConfig(origin)} label="Copy config" copied="Config copied" />
          <p className="text-sm text-muted-foreground">
            Ask the agent to list your databases. If the tools are missing, read the reason in your
            client&apos;s MCP server log. The bridge follows the next release. To stay on one build,
            replace next with a version number.
          </p>
        </Step>
      </ol>
      <ConnectedAgents />
    </div>
  );
}
