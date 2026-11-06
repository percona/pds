import { useMutation, useQuery } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { useParams, useSearchParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import {
  CommandBlock,
  GuideRefusal,
  Mono,
  Step,
  fetchConnectGuide,
  isEnvironmentName,
  issueJoinToken,
  joinEnvironmentCommand,
  useEnvironments,
} from "@/features/environments";
import { useActingTeam, useTeamToCreateIn } from "@/features/teams";

/**
 * Moves an environment that already joined to the release this control plane
 * runs. The customer runs the command, so this page mints a token for it and
 * says what the command will and will not do. It reads nothing about the
 * environment's current version: the command checks that where it runs.
 */
export function UpdateEnvironmentPage() {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const team = params.get("team");

  if (!isEnvironmentName(name)) {
    return (
      <GuideRefusal
        title="Update an environment"
        problem="The name in this link is not an environment name, so no command is shown."
      />
    );
  }

  // Keyed by the name and the Team so a command minted for one environment is
  // gone when the route moves to another.
  return <UpdateGuide key={`${name}?${team ?? ""}`} name={name} team={team} />;
}

function UpdateGuide({ name, team }: { name: string; team: string | null }) {
  const { environments, isPending, resolved, error } = useEnvironments();
  const acting = useActingTeam();
  // While every Team is shown each row names its Team. In the view of one Team
  // the rows do not, and that Team is the one the console acts in.
  const matches = environments.filter(
    (candidate) =>
      candidate.name === name &&
      (team === null || (acting.allTeams ? candidate.team === team : team === acting.team)),
  );
  const environment = matches[0];
  const inTeam = useTeamToCreateIn(environment?.team);
  const guide = useQuery({
    queryKey: ["environments", "connect-guide"],
    queryFn: ({ signal }) => fetchConnectGuide(signal),
  });
  const mint = useMutation({
    mutationFn: (forTeam: string | undefined) => issueJoinToken(name, forTeam),
  });

  // A token minted for a name the control plane does not know would register a
  // new environment, so the command waits for the list to say this one exists
  // and has reported in.
  if (!resolved && error) {
    return (
      <GuideRefusal
        title={`Update ${name}`}
        problem={`The environment list could not be read. ${error.message}`}
      />
    );
  }
  if (isPending) {
    return <GuideRefusal title={`Update ${name}`} problem="Reading environments..." />;
  }
  if (matches.length > 1) {
    return (
      <GuideRefusal
        title={`Update ${name}`}
        problem={`More than one Team has an environment named ${name}. Open Update from the row of the one you mean.`}
      />
    );
  }
  if (!environment) {
    return (
      <GuideRefusal
        title={`Update ${name}`}
        problem={`${name} is not an environment on this control plane, so no command is shown.`}
      />
    );
  }
  if (!environment.lastSeen) {
    return (
      <GuideRefusal
        title={`Update ${name}`}
        problem={`${name} has not connected yet, so there is nothing to update. Run its join command from Create an environment.`}
      />
    );
  }

  const hubUrl = mint.data?.hubUrl?.trim() || guide.data?.hubUrl?.trim() || "";
  const command =
    mint.data && hubUrl !== ""
      ? joinEnvironmentCommand({ hubUrl, token: mint.data.token, update: true })
      : "";

  return (
    <div className="space-y-6">
      <PageHeader
        title={`Update ${name}`}
        description="Move this environment to the release this control plane runs."
      />

      <div className="space-y-6">
        <Step n={1} title="Point kubectl at its Kubernetes cluster">
          <p className="text-sm text-muted-foreground">
            Run the command where kubectl points at the Kubernetes cluster of <Mono>{name}</Mono>.
            Check with <Mono>kubectl config current-context</Mono>. It needs no AWS credentials and
            changes nothing in AWS.
          </p>
        </Step>

        <Step n={2} title="Run this command">
          <div className="flex items-start gap-2">
            <TriangleAlert
              className="mt-0.5 size-4 shrink-0 text-warning"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <p className="text-sm">
              Updating in place is not a tested upgrade path. Back up your databases first.
            </p>
          </div>
          <p className="text-sm text-muted-foreground">
            It moves the platform on the environment as one: OpenEverest, the database providers and
            what connects it here. It stops without changing anything when a database or backup is
            busy, or when the update would go back to an older version.
          </p>
          {inTeam.field}
          {command === "" ? (
            <div className="space-y-3">
              <Button
                type="button"
                disabled={mint.isPending || guide.isPending}
                loading={mint.isPending}
                onClick={() => mint.mutate(inTeam.team)}
              >
                {mint.isPending ? "Creating..." : "Create command"}
              </Button>
              {mint.error ? <p className="text-sm text-destructive">{mint.error.message}</p> : null}
              {guide.error ? (
                <p className="text-sm text-destructive">
                  Settings could not be read. {guide.error.message}
                </p>
              ) : null}
              {mint.data && hubUrl === "" ? (
                <p className="text-sm text-muted-foreground">
                  This control plane has no public address yet, so the command cannot be printed.
                  Contact Percona support.
                </p>
              ) : null}
            </div>
          ) : (
            <div className="space-y-3">
              <CommandBlock command={command} />
              <p className="text-sm text-muted-foreground">
                The command contains a join secret that works once. Treat it like a password: anyone
                who runs it connects an environment to this control plane.
              </p>
            </div>
          )}
        </Step>

        <Step n={3} title="Wait for it to finish">
          <p className="text-sm text-muted-foreground">
            The command prints each phase as it goes and waits for the environment to report in. If
            it stops, it says where, and a new command from this page tries again.
          </p>
        </Step>
      </div>
    </div>
  );
}
