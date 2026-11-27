import { CircleAlert, Plus, SearchX, Server, UserPlus, Users } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Notice } from "@/components/ui/notice";
import { Skeleton } from "@/components/ui/skeleton";
import { StatusPill } from "@/features/databases";
import { environmentPill } from "@/features/environments";
import {
  DeleteTeamDialog,
  TeamNameDialog,
  teamPath,
  useActingTeam,
  useTeamEnvironments,
  useTeams,
} from "@/features/teams";
import {
  InvitationList,
  InviteDialog,
  IssuedLinks,
  MemberList,
  useInviteOutcomes,
} from "@/features/users";

/** One Team: its users, the invites that land in it, and the environments it owns. */
export function TeamPage() {
  const name = useParams().name ?? "";
  const navigate = useNavigate();
  const teams = useTeams();
  const [inviting, setInviting] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const { outcomes, add, addIssued, dismiss } = useInviteOutcomes();

  if (teams.isPending) {
    return (
      <div className="space-y-8" data-testid="team-loading">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  if (teams.error) {
    return (
      <Notice
        icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="Teams could not be read"
        detail={teams.error.message}
        action={
          <Button variant="outline" loading={teams.isFetching} onClick={() => void teams.refetch()}>
            Try again
          </Button>
        }
      />
    );
  }

  const team = teams.data.find((item) => item.name === name);

  if (team === undefined) {
    return (
      <Notice
        icon={<SearchX className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="No such team"
        detail={`Nothing called ${name} is in your organization.`}
        action={
          <Button variant="outline" asChild>
            <Link to="/teams">View teams</Link>
          </Button>
        }
      />
    );
  }

  const users = team.members.length;

  return (
    <div className="space-y-8">
      <PageHeader
        title={team.name}
        description={`${users} ${users === 1 ? "user" : "users"} in this team.`}
        actions={
          <>
            <Button type="button" onClick={() => setInviting(true)}>
              <UserPlus className="size-4" strokeWidth={1.5} aria-hidden="true" />
              Invite people
            </Button>
            <Button type="button" variant="outline" onClick={() => setRenaming(true)}>
              Rename
            </Button>
            <Button type="button" variant="outline" onClick={() => setDeleting(true)}>
              Delete
            </Button>
          </>
        }
      />

      <InviteDialog open={inviting} onOpenChange={setInviting} team={team.name} onSent={add} />
      <TeamNameDialog
        open={renaming}
        onOpenChange={setRenaming}
        team={team.name}
        onSaved={(renamed) => void navigate(teamPath(renamed), { replace: true })}
      />
      <DeleteTeamDialog
        team={team.name}
        open={deleting}
        onOpenChange={setDeleting}
        onDeleted={() => void navigate("/teams", { replace: true })}
      />

      <IssuedLinks outcomes={outcomes} onDismiss={dismiss} />

      <section className="space-y-4" aria-labelledby="team-users-heading">
        <h2 id="team-users-heading" className="text-base font-semibold">
          Users
        </h2>
        <MemberList
          team={team.name}
          empty={
            <Notice
              icon={<Users className="size-5 text-muted-foreground" strokeWidth={1.5} />}
              title={`No users in ${team.name} yet`}
              detail="Invite someone into this team, or move a user here from another team."
            />
          }
        />
      </section>

      <section className="space-y-4" aria-labelledby="team-pending-heading">
        <h2 id="team-pending-heading" className="text-base font-semibold">
          Pending invites
        </h2>
        <InvitationList team={team.name} onIssued={addIssued} />
      </section>

      <TeamEnvironments team={team.name} />
    </div>
  );
}

function TeamEnvironments({ team }: { team: string }) {
  const environments = useTeamEnvironments();
  const acting = useActingTeam();
  // The create page makes an environment in the Team the console acts in, or
  // asks which while every Team is shown, so it is offered only where it lands here.
  const canCreate = acting.allTeams || acting.team === team;
  const owned = environments.data?.filter((environment) => environment.team === team) ?? [];

  return (
    <section className="space-y-4" aria-labelledby="team-environments-heading">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id="team-environments-heading" className="text-base font-semibold">
          Environments
        </h2>
        {canCreate ? (
          <Button variant="outline" asChild>
            <Link to="/environments/new">
              <Plus className="size-4" strokeWidth={1.5} aria-hidden="true" />
              Create environment
            </Link>
          </Button>
        ) : null}
      </div>
      {canCreate ? null : (
        <p className="text-sm text-muted-foreground">
          A new environment belongs to the team the console acts in. Switch to {team} to create one
          here.
        </p>
      )}

      {environments.isPending ? (
        <Card className="space-y-3 p-4" data-testid="team-environments-loading">
          <Skeleton className="h-4 w-1/3" />
          <Skeleton className="h-4 w-1/4" />
        </Card>
      ) : environments.error ? (
        <Notice
          icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          title="Environments could not be read"
          detail={environments.error.message}
          action={
            <Button
              variant="outline"
              loading={environments.isFetching}
              onClick={() => void environments.refetch()}
            >
              Try again
            </Button>
          }
        />
      ) : owned.length === 0 ? (
        <Notice
          icon={<Server className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          title="No environments yet"
          detail={`An environment belongs to the team it was created for. Create one for ${team} and its users can create databases on it.`}
        />
      ) : (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-border" aria-label={`Environments of ${team}`}>
            {owned.map((environment) => {
              const pill = environmentPill(environment);

              return (
                <li
                  key={environment.name}
                  className="flex items-center justify-between gap-4 px-4 py-3"
                >
                  <span className="min-w-0 truncate font-mono text-sm font-medium">
                    {environment.name}
                  </span>
                  <StatusPill kind={pill.kind} label={pill.label} />
                </li>
              );
            })}
          </ul>
        </Card>
      )}
    </section>
  );
}
