import { Check, ChevronRight } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
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
  "Work with PDS from Claude Code, Cursor or any MCP client. An agent signs in as you, with no password in your client's settings, does only what you allow, and what it does is recorded as yours.";

const defaultHost = new URL(defaultOrigin).host;

function Address({ origin }: { origin: string }) {
  return <CommandBlock command={mcpAddress(origin)} label="Copy address" copied="Address copied" />;
}

/** A card with its own heading, so each way in is a region a reader can find by name. */
function WayIn({
  title,
  subtitle,
  badge,
  level = 3,
  children,
}: {
  title: string;
  subtitle: string;
  badge?: string;
  level?: 2 | 3;
  children: ReactNode;
}) {
  const titleId = useId();
  const Heading = level === 2 ? "h2" : "h3";

  return (
    <Card role="region" aria-labelledby={titleId} className="min-w-0 space-y-3 p-5">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <Heading id={titleId} className="text-base font-semibold">
            {title}
          </Heading>
          <p className="text-xs text-muted-foreground">{subtitle}</p>
        </div>
        {badge ? <Badge variant="outline">{badge}</Badge> : null}
      </div>
      {children}
    </Card>
  );
}

function More({ summary, children }: { summary: string; children: ReactNode }) {
  return (
    <details className="group border-t border-border pt-3">
      <summary className="flex cursor-pointer list-none items-center gap-1.5 rounded-md text-sm font-medium focus-visible:outline-2 focus-visible:outline-primary [&::-webkit-details-marker]:hidden">
        <ChevronRight
          className="size-4 shrink-0 group-open:rotate-90"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        {summary}
      </summary>
      <div className="mt-3 space-y-3">{children}</div>
    </details>
  );
}

export function AgentsPage() {
  const [access, setAccess] = useState<Access>("read");
  const origin = window.location.origin;
  const selfHosted = origin !== defaultOrigin;
  const pluginsId = useId();

  return (
    <div className="max-w-4xl animate-fade-in space-y-6">
      <PageHeader title="AI agents" description={intro} />
      <AiSettingsSummary />

      <section aria-labelledby={pluginsId} className="space-y-3">
        <h2 id={pluginsId} className="text-base font-semibold">
          Plugins
        </h2>
        <div className="grid gap-4 md:grid-cols-2">
          <WayIn
            title="Claude Code"
            subtitle="The pds plugin, with a local data server for MySQL and PostgreSQL"
            badge="Recommended"
          >
            <p className="text-sm">Run this in a terminal:</p>
            <CommandBlock
              command={claudeCodeInstall}
              label="Copy install commands"
              copied="Install commands copied"
            />
            {selfHosted ? (
              <>
                <p className="text-sm">
                  The plugin signs in to <span className="font-mono">{defaultHost}</span> unless you
                  start Claude Code with this control plane&apos;s address.
                </p>
                <CommandBlock
                  command={claudeCodeStart(origin)}
                  label="Copy start command"
                  copied="Start command copied"
                />
              </>
            ) : null}
            <p className="text-sm text-muted-foreground">
              Then open /mcp in Claude Code, choose plugin:pds:pds and sign in in your browser.
              {selfHosted ? null : (
                <>
                  {" "}
                  The plugin already points at <span className="font-mono">{defaultHost}</span>, so
                  there is no address to set.
                </>
              )}
            </p>
            <More summary="Update the plugin">
              <CommandBlock
                command={claudeCodeUpdate}
                label="Copy update commands"
                copied="Update commands copied"
              />
              <p className="text-xs text-muted-foreground">Restart Claude Code after the update.</p>
            </More>
          </WayIn>

          <WayIn title="Cursor" subtitle="The same pds plugin, from the Cursor marketplace">
            <ol className="list-decimal space-y-1.5 pl-5 text-sm">
              <li>In Cursor, open Customize.</li>
              <li>
                Add the marketplace <span className="font-mono">percona-pds</span> from the
                repository <span className="font-mono">percona/pds</span>.
              </li>
              <li>
                Install <span className="font-mono">pds</span> and sign in in your browser.
              </li>
            </ol>
            {selfHosted ? (
              <>
                <p className="text-sm">
                  The plugin points at <span className="font-mono">{defaultHost}</span>. Add this
                  control plane as a remote MCP server by hand with this address, and sign in in
                  your browser.
                </p>
                <Address origin={origin} />
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                It already points at <span className="font-mono">{defaultHost}</span>.
              </p>
            )}
          </WayIn>
        </div>
      </section>

      <WayIn
        level={2}
        title="Any MCP client"
        subtitle="Claude Desktop, VS Code and other clients that support remote servers"
      >
        <p className="text-sm">
          Add this address as a remote MCP server. In Claude Desktop that is Settings, Connectors,
          Add custom connector. Your client opens the browser to sign in. Check what the agent may
          do, then allow it.
        </p>
        <Address origin={origin} />
        <More summary="My client only runs a local command">
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
                    "rounded-lg border p-3 text-left text-sm focus-visible:outline-2 focus-visible:outline-primary",
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
        </More>
      </WayIn>

      <ConnectedAgents />
    </div>
  );
}
