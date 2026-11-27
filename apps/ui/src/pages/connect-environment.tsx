import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router";
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
        description="One command creates an EKS cluster, installs the platform, and connects it here as an environment."
      />

      <Link
        to="/environments/new"
        className="inline-block rounded-md text-sm font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        Let PDS create it in your AWS account instead
      </Link>

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
 * What the command will spend, and the region an empty answer falls back to
 * when the machine running it has no AWS region of its own. The command asks
 * before it creates anything. The region named here is only that fallback.
 */
function Prerequisites({ region }: { region?: string }) {
  return (
    <div className="space-y-1.5">
      <p className="text-sm text-muted-foreground">
        Run this on a machine with aws, eksctl, kubectl, helm, and python3. You do not paste an
        address.
      </p>
      <p className="text-sm text-muted-foreground">
        It asks which AWS region to use. An empty answer uses the region in your AWS configuration,
        or <span className="font-mono">{region ?? "us-west-2"}</span> when you have not set one. It
        then creates an EKS cluster there with four t3.2xlarge nodes and an S3 bucket, which cost
        money, then installs Percona Database Service and connects it here as an environment. A
        Kubernetes cluster that already runs Percona Database Service keeps that release rather than
        gaining a second one, and its own sign in page turns off. Add{" "}
        <span className="font-mono">--local</span> to join a Kubernetes cluster you already run
        instead, which creates nothing in AWS.
      </p>
    </div>
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
