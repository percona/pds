import { Check, CircleHelp, Plug } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useAgentSessions } from "@/features/agent-sessions";
import { accountName, connectAccountPath, type AwsAccount } from "@/features/aws-accounts";
import { engineChoicePhrase } from "@/features/databases";
import type { Environment } from "@/features/environments";
import { useFeature } from "@/features/flags";
import { cn } from "@/lib/utils";
import { createDatabasePath, takesDatabases } from "../environment-phase";

type SetupChecklistProps = {
  organization?: string;
  environments: Environment[];
  accounts: AwsAccount[];
};

const required = 2;

/**
 * What a new organization does before it has a database. The checklist ends
 * with the first database, so connecting to one is not a step here: the
 * database page leads with it.
 */
export function SetupChecklist({ organization, environments, accounts }: SetupChecklistProps) {
  const mcpOn = useFeature("mcp");
  const connectorsOn = useFeature("connections");
  const chatOn = useFeature("chat");
  const account = accounts.find((candidate) => candidate.state === "ready");
  const hasEnvironment = environments.length > 0;
  const target = environments.find(takesDatabases);
  const done = hasEnvironment ? 1 : 0;

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
      <Card className="overflow-hidden">
        <div className="space-y-2 border-b border-border px-5 py-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="text-base font-semibold">
              {organization ? `Set up ${organization}` : "Set up PDS"}
            </h2>
            <p className="text-xs text-muted-foreground">
              <span className="font-mono">{done}</span> of{" "}
              <span className="font-mono">{required}</span> required steps done
            </p>
          </div>
          <div
            role="progressbar"
            aria-label="Setup progress"
            aria-valuemin={0}
            aria-valuemax={required}
            aria-valuenow={done}
            className="h-2 overflow-hidden rounded-full bg-muted"
          >
            <div className="h-full bg-primary" style={{ width: `${(done / required) * 100}%` }} />
          </div>
        </div>
        <ol className="divide-y divide-border">
          <Step
            number={1}
            done={hasEnvironment}
            title="Create an environment"
            body={
              account ? (
                <>
                  AWS account <span className="font-mono">{accountName(account)}</span> is
                  connected. PDS builds an EKS cluster in it and connects it here.
                </>
              ) : (
                "Connect an AWS account, and PDS builds an EKS cluster in it and connects it here."
              )
            }
            action={
              account ? (
                <Button asChild size="sm">
                  <Link to="/environments/new">Create an environment</Link>
                </Button>
              ) : (
                <>
                  <Button asChild size="sm">
                    <Link to={connectAccountPath}>Connect an AWS account</Link>
                  </Button>
                  <Button asChild size="sm" variant="outline">
                    <Link to="/environments/new">Create an environment</Link>
                  </Button>
                </>
              )
            }
          />
          <Step
            number={2}
            done={false}
            title="Create a database"
            body={
              target ? (
                <>
                  {engineChoicePhrase} on <span className="font-mono">{target.name}</span>.
                </>
              ) : hasEnvironment ? (
                "Available once your environment is connected and reporting."
              ) : (
                `${engineChoicePhrase}. Needs an environment first.`
              )
            }
            action={
              target ? (
                <Button asChild size="sm">
                  <Link to={createDatabasePath(target.name)}>Create database</Link>
                </Button>
              ) : (
                <Button size="sm" variant="outline" disabled>
                  Create database
                </Button>
              )
            }
          />
          {mcpOn ? <AgentStep /> : null}
        </ol>
      </Card>
      <div className="space-y-6">
        {connectorsOn && chatOn ? (
          <Card className="space-y-3 px-5 py-4">
            <div className="flex items-center gap-2">
              <Plug className="size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
              <h2 className="text-base font-semibold">Connectors</h2>
            </div>
            <p className="text-sm text-muted-foreground">
              Chat needs an Anthropic or Cursor key. An admin adds it once for the organization.
            </p>
            <Button asChild size="sm" variant="outline">
              <Link to="/connectors">Open Connectors</Link>
            </Button>
          </Card>
        ) : null}
        <Card className="space-y-3 px-5 py-4">
          <div className="flex items-center gap-2">
            <CircleHelp
              className="size-4 text-muted-foreground"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <h2 className="text-base font-semibold">What is an environment?</h2>
          </div>
          <p className="text-sm text-muted-foreground">
            A Kubernetes cluster connected to PDS. Your databases run there, in your account, and
            PDS manages them from here.
          </p>
        </Card>
      </div>
    </div>
  );
}

/**
 * Done once an agent is signed in as this person. An admin sees every member's
 * agents, so only the person's own count.
 */
function AgentStep() {
  const sessions = useAgentSessions();
  const signedIn = (sessions.data ?? []).some((session) => session.mine);

  return (
    <Step
      number={3}
      done={signedIn}
      optional
      title="Connect an AI agent"
      body="Let Claude Code, Cursor or another MCP client work with your databases. It signs in as you."
      action={
        <Button asChild size="sm" variant="outline">
          <Link to="/agents">Set up</Link>
        </Button>
      }
    />
  );
}

function Step({
  number,
  done,
  title,
  body,
  action,
  optional,
}: {
  number: number;
  done: boolean;
  title: string;
  body: ReactNode;
  action: ReactNode;
  optional?: boolean;
}) {
  return (
    <li className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-3 px-5 py-4 sm:grid-cols-[auto_minmax(0,1fr)_auto] sm:items-center">
      <span
        className={cn(
          "flex size-6 items-center justify-center rounded-full border font-mono text-xs",
          done
            ? "border-transparent bg-success text-success-foreground"
            : "border-border text-muted-foreground",
        )}
      >
        {done ? <Check className="size-3.5" strokeWidth={1.5} aria-hidden="true" /> : number}
      </span>
      <div className="min-w-0 space-y-0.5">
        <p className="flex flex-wrap items-center gap-2 text-sm font-medium">
          {title}
          {optional ? <Badge variant="outline">Optional</Badge> : null}
        </p>
        <p className="text-sm text-muted-foreground">{body}</p>
      </div>
      <div className="col-start-2 flex flex-wrap gap-2 sm:col-start-3 sm:justify-end">
        {done ? <span className="text-xs text-muted-foreground">Done</span> : action}
      </div>
    </li>
  );
}
