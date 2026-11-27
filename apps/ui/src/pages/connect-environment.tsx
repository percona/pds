import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
import { CircleDollarSign } from "lucide-react";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  CommandBlock,
  environmentsKey,
  fetchConnectGuide,
  isEnvironmentName,
  issueJoinToken,
  reservedEnvironmentName,
  updatePath,
  useEnvironments,
} from "@/features/environments";
import { useTeamToCreateIn } from "@/features/teams";
import { cn } from "@/lib/utils";

export function ConnectEnvironmentPage() {
  const queryClient = useQueryClient();
  const { environments } = useEnvironments();
  const guide = useQuery({
    queryKey: ["environments", "connect-guide"],
    queryFn: ({ signal }) => fetchConnectGuide(signal),
  });
  const [params] = useSearchParams();
  const [name, setName] = useState(() => params.get("name") ?? "");
  const [touched, setTouched] = useState(false);
  const inTeam = useTeamToCreateIn();
  const join = useMutation({
    mutationFn: (input: { environment: string; team: string | undefined }) =>
      issueJoinToken(input.environment, input.team),
  });

  const validName = isEnvironmentName(name) && !reservedEnvironmentName(name);
  const canMint = validName;
  const nameError = reservedEnvironmentName(name)
    ? "That name is taken by a page in this app. Pick another."
    : touched && name.trim() !== "" && !isEnvironmentName(name)
      ? "Use lowercase letters, digits, and dashes, starting and ending with a letter or digit."
      : undefined;
  // While every Team is shown the list holds each Team's rows, and a name is
  // only taken within the Team the command is for.
  const existing = environments.find(
    (environment) =>
      environment.name === name && (inTeam.team === undefined || environment.team === inTeam.team),
  );
  const hubUrl = guide.data?.hubUrl?.trim() ?? "";
  const gitUrl = guide.data?.gitUrl?.trim() ?? "";
  const githubOrg = guide.data?.githubOrg?.trim() ?? "";
  const gitReady = gitUrl !== "" || githubOrg !== "";
  const connected = existing?.status === "connected";
  // A command is minted for one Team. Choosing another leaves it unusable.
  const minted = join.variables?.team === inTeam.team ? join.data : undefined;
  const command = minted?.command ?? "";

  return (
    <div className="space-y-6">
      <PageHeader
        title="Create an environment"
        description="One command creates an EKS cluster in your AWS account, installs the platform, and connects it here as an environment."
      />

      <ModeSwitch mode="command" name={name} />

      <div className="space-y-6">
        <Step n={1} title="Name this environment">
          <Field
            id="environment-name"
            label="Environment name"
            help="Lowercase letters, digits, and dashes. Also the EKS cluster name."
            error={nameError}
          >
            {(props) => (
              <Input
                {...props}
                className="font-mono"
                value={name}
                autoComplete="off"
                onBlur={() => setTouched(true)}
                onChange={(event) => {
                  setName(event.target.value);
                  join.reset();
                }}
              />
            )}
          </Field>
          {inTeam.field}
        </Step>

        <Step n={2} title="Run this command">
          {existing ? (
            <p className="text-sm text-muted-foreground">
              {existing.name} is already connected. Run the command again to repair its connection
              {existing.lastSeen ? (
                <>
                  , or{" "}
                  <Link
                    to={updatePath(existing.name, existing.team)}
                    className="rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                  >
                    update it in place
                  </Link>
                </>
              ) : null}
              .
            </p>
          ) : null}
          {guide.isPending ? (
            <p className="text-sm text-muted-foreground">Reading settings...</p>
          ) : null}
          {guide.error ? (
            <p className="text-sm text-destructive">
              Settings could not be read. {guide.error.message}
            </p>
          ) : null}
          {!guide.error && hubUrl === "" && !guide.isPending ? (
            <p className="text-sm text-muted-foreground">
              This control plane has no public address yet, so an environment cannot join. Contact
              Percona support.
            </p>
          ) : null}
          {!guide.error && hubUrl !== "" && !gitReady && !guide.isPending ? (
            <p className="text-sm text-muted-foreground">
              This control plane cannot store configuration for an environment yet, so an
              environment cannot join. Contact Percona support.
            </p>
          ) : null}
          {!guide.error && hubUrl !== "" && gitReady && command === "" ? (
            <div className="space-y-3">
              <Prerequisites region={guide.data?.clusterRegion} />
              <Button
                type="button"
                disabled={!canMint || join.isPending}
                loading={join.isPending}
                onClick={() => join.mutate({ environment: name, team: inTeam.team })}
              >
                {join.isPending ? "Creating..." : "Create command"}
              </Button>
              {join.error ? <p className="text-sm text-destructive">{join.error.message}</p> : null}
            </div>
          ) : null}
          {command !== "" ? (
            <div className="space-y-3">
              <Prerequisites region={minted?.region ?? guide.data?.clusterRegion} />
              <CommandBlock command={command} />
              <p className="text-sm text-muted-foreground">
                The command contains a join secret. Treat it like a password: anyone who runs it
                connects an environment to this control plane.
              </p>
            </div>
          ) : null}
        </Step>

        <Step n={3} title="Wait for it to appear">
          {connected ? (
            <div className="space-y-3">
              <p className="text-sm font-medium">{name} is connected.</p>
              <Button asChild>
                <Link to={`/environments/${encodeURIComponent(name)}`}>Open environment</Link>
              </Button>
            </div>
          ) : (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                After the command finishes, this page lists the environment as soon as it reports
                in.
              </p>
              <Button
                type="button"
                variant="outline"
                onClick={() => void queryClient.invalidateQueries({ queryKey: environmentsKey })}
              >
                Check now
              </Button>
            </div>
          )}
        </Step>
      </div>
    </div>
  );
}

/**
 * What the command needs and what it spends. The command asks for the region
 * before it creates anything; the region named here is only what an empty
 * answer falls back to on a machine with no AWS region of its own.
 */
function Prerequisites({ region }: { region?: string }) {
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">
        Run this on a machine with aws, eksctl, kubectl, helm, and python3. It asks which AWS region
        to use, with <span className="font-mono">{region ?? "us-west-2"}</span> when you have none
        set, then creates an EKS cluster there with four t3.2xlarge nodes and an S3 bucket, installs
        Percona Database Service and connects it here.
      </p>
      <CostNotice>
        Everything is created in your AWS account, and AWS bills you for it. Disconnecting an
        environment later does not delete it.
      </CostNotice>
    </div>
  );
}

/** The warning that what PDS or the command builds is billed by AWS. */
export function CostNotice({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-border px-4 py-3 text-sm">
      <CircleDollarSign
        className="mt-0.5 size-4 shrink-0 text-warning"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <p>{children}</p>
    </div>
  );
}

type CreateMode = "managed" | "command";

/**
 * PDS building the environment is the default. Running the command yourself is
 * the other way, one switch away, and the name typed so far goes with it.
 */
export function ModeSwitch({ mode, name }: { mode: CreateMode; name: string }) {
  const named = name ? `name=${encodeURIComponent(name)}` : "";
  const choices: { value: CreateMode; label: string; to: string }[] = [
    {
      value: "managed",
      label: "PDS creates it in your AWS account",
      to: `/environments/new${named ? `?${named}` : ""}`,
    },
    {
      value: "command",
      label: "Run a command yourself",
      to: `/environments/new?mode=command${named ? `&${named}` : ""}`,
    },
  ];

  return (
    <nav
      aria-label="How to create it"
      className="flex w-full flex-col gap-1 rounded-lg border border-border p-1 sm:inline-flex sm:w-auto sm:flex-row"
    >
      {choices.map((choice) => (
        <Link
          key={choice.value}
          to={choice.to}
          replace
          aria-current={choice.value === mode ? "page" : undefined}
          className={cn(
            "rounded-md px-3 py-1.5 text-sm font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
            choice.value === mode
              ? "bg-accent text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {choice.label}
        </Link>
      ))}
    </nav>
  );
}

export function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <section className="space-y-4">
      <Card className="space-y-4 px-5 py-5">
        <h2 className="text-base font-semibold">
          {n}. {title}
        </h2>
        <div className="space-y-4">{children}</div>
      </Card>
    </section>
  );
}
