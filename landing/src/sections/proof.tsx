import { Bot, ExternalLink } from "lucide-react";
import openeverestLogo from "@/assets/integrations/openeverest.png";
import { SectionHeading } from "@/components/section-heading";
import { Badge } from "@/components/ui/badge";
import { engineNames } from "@/lib/configurator";
import { sectionIds } from "@/lib/site";
import { cn } from "@/lib/utils";
import { AgentTerminal } from "./agent-terminal";

// Every figure is rounded down and links to the public page it was read from. Stars count interest in the open source projects PDS is built on,
// not customers of PDS. The Operator for PostgreSQL shows no license because
// none was confirmed.
const integrations = [
  {
    name: "OpenEverest",
    figure: "+900 GitHub Stars",
    license: "Apache-2.0",
    href: "https://github.com/openeverest/openeverest",
    lead: true,
  },
  {
    name: "Percona Operator for MySQL",
    figure: "+600 GitHub Stars",
    license: "Apache-2.0",
    href: "https://github.com/percona/percona-xtradb-cluster-operator",
  },
  {
    name: "Percona Operator for PostgreSQL",
    figure: "+300 GitHub Stars",
    href: "https://github.com/percona/percona-postgresql-operator",
  },
];

const facts = [
  "Since 2006 working with open source databases",
  "+3,000 consulting and support customers",
];

const textLink =
  "rounded-sm font-medium text-foreground underline underline-offset-4 hover:text-primary";

export function Proof() {
  return (
    <section
      id={sectionIds.proof}
      aria-labelledby="proof-title"
      className="border-y border-border bg-muted/40 px-6 py-14 sm:py-16"
    >
      <div className="mx-auto max-w-6xl space-y-8">
        <SectionHeading id="proof-title" title="No lock-in" />

        <div className="space-y-3 text-base">
          <p className="font-medium">Built on open source:</p>
          <ul className="list-disc space-y-2 pl-6 text-muted-foreground">
            <li>
              <a href="https://www.percona.com/cloud-native/" rel="noreferrer" className={textLink}>
                Percona Operators
              </a>{" "}
              for {engineNames("and")}, and{" "}
              <a href="https://www.percona.com/monitoring/" rel="noreferrer" className={textLink}>
                PMM
              </a>{" "}
              for monitoring.
            </li>
            <li>
              <a href="http://openeverest.io" rel="noreferrer" className={textLink}>
                OpenEverest
              </a>
              , the open-source database management platform that provides the underlying
              Kubernetes-based database provisioning and management capabilities.
            </li>
          </ul>
        </div>

        <ul aria-label="Open source projects" className="grid gap-4 md:grid-cols-2">
          {integrations.map((item) => (
            <li key={item.name} className={cn(item.lead && "md:col-span-2")}>
              <a
                href={item.href}
                rel="noreferrer"
                className={cn(
                  "flex h-full gap-5 rounded-lg border border-border bg-card p-5 transition-colors hover:bg-accent",
                  item.lead ? "flex-col sm:flex-row sm:items-center" : "flex-col justify-center",
                )}
              >
                {item.lead ? (
                  // The mark's dark lettering vanishes on the dark theme, so it keeps a light tile.
                  <span className="flex h-20 w-full shrink-0 items-center justify-center rounded-md border border-border bg-logo-ground px-4 sm:w-64">
                    <img
                      src={openeverestLogo}
                      alt="OpenEverest logo"
                      width={1222}
                      height={425}
                      className="h-14 w-auto"
                    />
                  </span>
                ) : null}
                <span className="flex min-w-0 flex-1 flex-col gap-2">
                  <span className="flex items-start justify-between gap-2">
                    <span
                      className={cn(
                        "font-semibold tracking-tight",
                        item.lead ? "text-2xl" : "text-base",
                      )}
                    >
                      {item.name}
                    </span>
                    <ExternalLink
                      className="mt-1 size-4 shrink-0 text-muted-foreground"
                      strokeWidth={1.5}
                      aria-hidden="true"
                    />
                  </span>
                  <span className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <span className="text-sm text-muted-foreground">{item.figure}</span>
                    {item.license ? <Badge variant="outline">{item.license}</Badge> : null}
                  </span>
                </span>
              </a>
            </li>
          ))}
        </ul>

        <ul className="flex flex-wrap gap-x-10 gap-y-3">
          {facts.map((fact) => (
            <li key={fact}>
              <a
                href="https://www.percona.com/about"
                rel="noreferrer"
                className="rounded-md text-sm font-medium hover:underline"
              >
                {fact}
              </a>
            </li>
          ))}
        </ul>

        <div
          id="ai-agents"
          className="grid items-center gap-6 rounded-lg border border-primary bg-card p-4 sm:p-6 md:grid-cols-2 md:gap-10"
        >
          <div className="space-y-3">
            <p className="inline-flex items-center gap-1.5 text-sm font-medium text-primary">
              <Bot className="size-4" strokeWidth={1.5} aria-hidden="true" />
              AI agents
            </p>
            <h3 className="text-2xl font-semibold tracking-tight text-balance">
              Let an AI agent work on your databases
            </h3>
          </div>
          <figure className="overflow-hidden rounded-lg border border-border bg-muted/40">
            <figcaption className="sr-only">
              An agent calls list_databases and list_alerts, then reports one warning on orders.
            </figcaption>
            <AgentTerminal />
          </figure>
        </div>
      </div>
    </section>
  );
}
