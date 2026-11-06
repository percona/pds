import { useNavigate, useParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { UnreachableBanner, useEnvironments } from "@/features/environments";
import {
  CreateDatabaseForm,
  createPageDescription,
  databaseHref,
  useCreateDatabase,
} from "@/features/databases";
import { useSupportAvailability } from "@/features/support";
import { useTeamToCreateIn } from "@/features/teams";
import { formatTimestamp } from "@/lib/format";
import { NoSuchEnvironment } from "./databases";

/**
 * The create page. The form is offered once the destination environment is known.
 */
export function CreateDatabasePage() {
  const navigate = useNavigate();
  const { name: routeEnvironment } = useParams();
  const support = useSupportAvailability();
  const { environments, error: environmentsError, resolved } = useEnvironments();
  const environment = routeEnvironment
    ? environments.find((item) => item.name === routeEnvironment)
    : undefined;
  // The database runs on an environment of one Team, so while every Team is
  // shown the create goes to that Team and nothing is asked.
  const inTeam = useTeamToCreateIn(environment?.team);
  const create = useCreateDatabase(inTeam.team);
  const unknownEnvironment = Boolean(routeEnvironment && resolved && !environment);
  const unreachable = environment?.status === "unreachable";
  // An environment that has reported at least once can be written to while it is
  // offline: the create is a commit, and Flux applies it on reconnect. One that
  // has never reported is still being built, so nothing would pull the manifest
  // and the adapter refuses it too.
  const neverReported = Boolean(unreachable && !environment?.lastSeen);
  const canCreate = Boolean(environment) && !neverReported;

  // A failed first read is an error. A later blip keeps the form, because the
  // last snapshot still names the environment the create is for.
  if (environmentsError && !resolved) {
    return (
      <div className="mx-auto w-full max-w-xl space-y-8">
        <PageHeader title="Create database" description="The environment list could not be read." />
        <p className="text-sm text-muted-foreground">{environmentsError.message}</p>
      </div>
    );
  }

  if (!resolved) {
    return (
      <div className="mx-auto w-full max-w-xl space-y-8">
        <PageHeader
          title="Create database"
          description={
            routeEnvironment
              ? `${createPageDescription} On ${routeEnvironment}.`
              : createPageDescription
          }
        />
      </div>
    );
  }

  if (!routeEnvironment || unknownEnvironment) {
    return <NoSuchEnvironment name={routeEnvironment ?? ""} />;
  }

  return (
    <div className="mx-auto w-full max-w-xl space-y-8">
      <PageHeader
        title="Create database"
        description={`${createPageDescription} On ${routeEnvironment}.`}
      />

      {unreachable ? (
        <UnreachableBanner lastSeen={formatTimestamp(environment?.lastSeen) ?? undefined} />
      ) : null}

      {canCreate ? (
        <>
          <p className="text-sm text-muted-foreground">
            That environment is not checked before a create. If it is missing its database engine or
            its backup storage, the database stays in Creating rather than being refused here.
          </p>
          {inTeam.field}
          <CreateDatabaseForm
            submitting={create.isPending}
            error={create.error?.message ?? null}
            supportConnected={support.connected}
            supportPortalUrl={support.portalUrl}
            environment={routeEnvironment}
            onSubmit={(input) =>
              create.mutate(input, {
                onSuccess: (database) =>
                  void navigate(databaseHref(database.id), { state: { created: true } }),
              })
            }
          />
        </>
      ) : null}
    </div>
  );
}
