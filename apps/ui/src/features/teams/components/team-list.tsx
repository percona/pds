import { CircleAlert, Pencil, Trash2, Users } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Notice } from "@/components/ui/notice";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useTeamEnvironments, useTeams } from "../hooks/use-teams";
import { teamPath } from "../team-path";
import { DeleteTeamDialog } from "./delete-team-dialog";
import { TeamNameDialog } from "./team-name-dialog";

/**
 * The Teams of the Organization, each with its Users, the environments it owns
 * and the two things an admin does to it.
 */
export function TeamList() {
  const { data, error, isPending, isFetching, refetch } = useTeams();
  const environments = useTeamEnvironments();
  const [renaming, setRenaming] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<string | null>(null);

  if (isPending) {
    return <TeamListSkeleton />;
  }

  if (error) {
    return (
      <Notice
        icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="Teams could not be read"
        detail={error.message}
        action={
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        }
      />
    );
  }

  if (data.length === 0) {
    return (
      <Notice
        icon={<Users className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="No teams yet"
        detail="Create a team to group users and the environments they connect."
      />
    );
  }

  return (
    <>
      <Card className="overflow-hidden">
        <Table data-testid="team-list">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Team</TableHead>
              <TableHead>Users</TableHead>
              <TableHead>Environments</TableHead>
              <TableHead className="w-24">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((team) => {
              const owned = environments.data?.filter((item) => item.team === team.name) ?? [];

              return (
                <TableRow key={team.name}>
                  <TableCell>
                    <Link
                      to={teamPath(team.name)}
                      className="rounded-md font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                    >
                      {team.name}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <div className="space-y-0.5">
                      <p className="font-mono">{team.members.length}</p>
                      {team.members.length > 0 ? (
                        <p className="max-w-64 truncate text-xs text-muted-foreground">
                          {team.members.join(", ")}
                        </p>
                      ) : null}
                    </div>
                  </TableCell>
                  <TableCell>
                    {environments.isPending ? (
                      <Skeleton className="h-4 w-24" />
                    ) : environments.error ? (
                      <span className="text-xs text-muted-foreground">Could not be read</span>
                    ) : owned.length === 0 ? (
                      <span className="text-xs text-muted-foreground">None</span>
                    ) : (
                      <ul className="flex flex-wrap gap-1">
                        {owned.map((environment) => (
                          <li
                            key={environment.name}
                            className="rounded-full border border-border px-2 py-0.5 font-mono text-xs"
                          >
                            {environment.name}
                          </li>
                        ))}
                      </ul>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`Rename ${team.name}`}
                        onClick={() => setRenaming(team.name)}
                      >
                        <Pencil className="size-4" strokeWidth={1.5} aria-hidden="true" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`Delete ${team.name}`}
                        onClick={() => setDeleting(team.name)}
                      >
                        <Trash2 className="size-4" strokeWidth={1.5} aria-hidden="true" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Card>

      <TeamNameDialog
        open={renaming !== null}
        onOpenChange={(open) => !open && setRenaming(null)}
        team={renaming ?? undefined}
      />
      <DeleteTeamDialog
        team={deleting ?? ""}
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
      />
    </>
  );
}

// TeamListSkeleton matches the loaded row layout so the page does not shift.
function TeamListSkeleton() {
  return (
    <Card className="overflow-hidden" data-testid="team-list-loading">
      <div className="divide-y divide-border">
        {[0, 1, 2].map((row) => (
          <div key={row} className="flex items-center gap-4 px-3 py-3">
            <Skeleton className="h-4 flex-1" />
            <Skeleton className="h-4 w-10" />
            <Skeleton className="h-4 w-24" />
            <Skeleton className="h-4 w-16" />
          </div>
        ))}
      </div>
    </Card>
  );
}
