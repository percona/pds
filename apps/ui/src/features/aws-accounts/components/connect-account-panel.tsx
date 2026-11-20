import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  CircleAlert,
  CircleCheck,
  CircleDashed,
  CircleX,
  ExternalLink,
  Loader,
} from "lucide-react";
import { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { CommandBlock } from "@/features/environments";
import { cancelAwsConnectCommand, issueAwsConnectCommand } from "../api";
import { awsAccountsKey, useAwsAccount, useAwsAccounts } from "../hooks";
import { connectProgress, findConnectedAccount, type ConnectPhaseStatus } from "../progress";
import type { AwsAccount, AwsConnectCommand } from "../types";

const cloudShellUrl = "https://console.aws.amazon.com/cloudshell/home";

/** Splits "eu-west-1, us-east-1" into region names, dropping blanks. */
function regionList(text: string): string[] {
  return text
    .split(",")
    .map((region) => region.trim())
    .filter((region) => region !== "");
}

type ConnectAccountPanelProps = {
  /** Called once, when the command has connected the account and PDS can use it. */
  onConnected?: (account: AwsAccount) => void;
  /** How often the panel asks the control plane what the command reported. */
  pollMs?: number;
  /** The Team the account is for, when it is not the one the console acts in. */
  team?: string;
};

/**
 * One command connects an AWS account, and this panel follows it. The command
 * finds the account itself, so nothing here asks for an account ID.
 */
export function ConnectAccountPanel({
  onConnected,
  pollMs = 3000,
  team,
}: ConnectAccountPanelProps) {
  const queryClient = useQueryClient();
  const regionsId = useId();
  const [limit, setLimit] = useState(false);
  const [regions, setRegions] = useState("");
  const [issued, setIssued] = useState<{ command: AwsConnectCommand; since: Date } | null>(null);

  const issue = useMutation({
    mutationFn: () => issueAwsConnectCommand(limit ? regionList(regions) : [], team),
    onSuccess: (command) => setIssued({ command, since: new Date() }),
  });
  const cancel = useMutation({
    mutationFn: (id: string) => cancelAwsConnectCommand(id),
    onSuccess: () => {
      setIssued(null);
      issue.reset();
    },
  });

  const following = issued !== null;
  const list = useAwsAccounts({ pollMs, enabled: following });
  const found = issued ? findConnectedAccount(list.data ?? [], issued.since) : undefined;
  const one = useAwsAccount(found?.id, pollMs);
  const account = one.data ?? found;
  const progress = connectProgress(account, issued?.since ?? new Date());
  const existing =
    account !== undefined &&
    issued !== null &&
    Date.parse(account.createdAt) < issued.since.getTime() - 30_000;

  const connectedId = progress.done ? account?.id : undefined;
  useEffect(() => {
    if (connectedId !== undefined && account) {
      void queryClient.invalidateQueries({ queryKey: awsAccountsKey });
      onConnected?.(account);
    }
    // The account object changes on every poll. The id is what says it finished.
  }, [connectedId]);

  const regionsInvalid = limit && regionList(regions).length === 0;

  return (
    <div className="space-y-4">
      {!following ? (
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            One command in your AWS account connects it here. It finds the account by itself, so you
            do not type an account ID. Run it again at any time to repair or update the connection.
          </p>
          <div className="space-y-3">
            <label className="flex items-center gap-2 text-sm font-medium">
              <input
                type="checkbox"
                className="size-4 accent-primary"
                checked={limit}
                onChange={(event) => setLimit(event.target.checked)}
              />
              Limit the regions
            </label>
            {limit ? (
              <Field
                id={regionsId}
                label="Regions"
                help="Environments can be built only in these regions. Separate them with commas."
              >
                {(props) => (
                  <Input
                    {...props}
                    className="font-mono"
                    value={regions}
                    placeholder="eu-west-1, us-east-1"
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setRegions(event.target.value)}
                  />
                )}
              </Field>
            ) : null}
          </div>
          <Button
            type="button"
            disabled={regionsInvalid || issue.isPending}
            loading={issue.isPending}
            onClick={() => issue.mutate()}
          >
            {issue.isPending ? "Creating..." : "Create command"}
          </Button>
          {issue.error ? <p className="text-sm text-destructive">{issue.error.message}</p> : null}
        </div>
      ) : (
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            Run this command in AWS CloudShell, or in any terminal where the aws command is signed
            in as someone who can create IAM roles.
          </p>
          <CommandBlock command={issued.command.command} />
          <div className="flex flex-wrap items-center gap-2">
            <Button asChild variant="outline">
              <a href={cloudShellUrl} target="_blank" rel="noopener noreferrer">
                <ExternalLink strokeWidth={1.5} aria-hidden="true" />
                Open CloudShell
              </a>
            </Button>
            {!progress.done ? (
              <Button
                type="button"
                variant="ghost"
                loading={cancel.isPending}
                onClick={() => cancel.mutate(issued.command.id)}
              >
                Cancel command
              </Button>
            ) : null}
          </div>
          <p className="text-sm text-muted-foreground">
            The command contains a secret that works for one hour and for this Team only. Treat it
            like a password.
          </p>
          {cancel.error ? <p className="text-sm text-destructive">{cancel.error.message}</p> : null}

          <ol className="space-y-2" aria-label="Connect progress">
            {progress.phases.map((phase) => (
              <li key={phase.name} className="flex items-center gap-2 text-sm">
                <PhaseIcon status={phase.status} />
                <span>{phase.name}</span>
                <span className="text-xs text-muted-foreground">{phaseWords[phase.status]}</span>
              </li>
            ))}
          </ol>

          {existing && !progress.done ? (
            <p className="text-sm text-muted-foreground">
              This AWS account is already connected. The command updates its connection instead of
              adding a second one.
            </p>
          ) : null}
          {progress.failed ? (
            <Problem>
              A step failed and nothing more was created. If your user cannot create IAM roles, run
              the command again as an administrator. The command carries on from where it stopped.
            </Problem>
          ) : null}
          {progress.stopped ? (
            <Problem>
              The command stopped reporting. If the terminal closed, run the command again. It waits
              for the roles it started and carries on.
            </Problem>
          ) : null}
          {progress.done && account ? (
            <p className="flex items-center gap-2 text-sm font-medium">
              <CircleCheck className="size-4 text-success" strokeWidth={1.5} aria-hidden="true" />
              {`AWS account ${account.alias ?? account.accountId} is connected.`}
            </p>
          ) : null}
        </div>
      )}

      <WhatItAdds />
    </div>
  );
}

const phaseWords: Record<ConnectPhaseStatus, string> = {
  pending: "Waiting",
  running: "Running",
  succeeded: "Done",
  failed: "Failed",
};

function PhaseIcon({ status }: { status: ConnectPhaseStatus }) {
  const common = { className: "size-4", strokeWidth: 1.5, "aria-hidden": true } as const;
  if (status === "succeeded") {
    return <CircleCheck {...common} className="size-4 text-success" />;
  }
  if (status === "failed") {
    return <CircleX {...common} className="size-4 text-destructive" />;
  }
  if (status === "running") {
    return <Loader {...common} className="size-4 animate-soft-pulse text-muted-foreground" />;
  }

  return <CircleDashed {...common} className="size-4 text-muted-foreground" />;
}

function Problem({ children }: { children: string }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
    >
      <CircleAlert
        className="mt-0.5 size-4 shrink-0 text-destructive"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <span>{children}</span>
    </div>
  );
}

/** What the command puts in the account, so a person can judge it before running it. */
function WhatItAdds() {
  return (
    <details className="rounded-lg border border-border px-4 py-3">
      <summary className="cursor-pointer text-sm font-medium focus-visible:outline-2 focus-visible:outline-primary">
        What it adds to the account
      </summary>
      <div className="mt-3 space-y-2 text-sm text-muted-foreground">
        <ul className="list-disc space-y-1 pl-5">
          <li>One stack, with termination protection on.</li>
          <li>
            The role PDS assumes. It needs a secret only PDS holds, and it reaches only resources
            tagged <span className="font-mono">percona.com/pds</span>.
          </li>
          <li>The service role CloudFormation uses to build the stack.</li>
        </ul>
        <p>
          Another Team can connect the same AWS account. It gets its own roles, and neither Team can
          use the other&apos;s environments.
        </p>
        <p className="flex flex-wrap gap-4">
          <a
            className="rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            href="/api/internal/cloud-connection/connect.sh"
            target="_blank"
            rel="noopener noreferrer"
          >
            View the script
          </a>
          <a
            className="rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
            href="/api/internal/cloud-connection/template.yaml"
            target="_blank"
            rel="noopener noreferrer"
          >
            View the template
          </a>
        </p>
      </div>
    </details>
  );
}
