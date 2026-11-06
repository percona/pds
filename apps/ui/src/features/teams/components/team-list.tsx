import { CircleAlert, Pencil, Trash2, Users } from "lucide-react";
import { useState } from "react";
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
import { useTeams } from "../hooks/use-teams";
import { DeleteTeamDialog } from "./delete-team-dialog";
import { TeamNameDialog } from "./team-name-dialog";

/** The Teams of the Organization, each with its Users and the two things an admin does to it. */
export function TeamList() {
  const { data, error, isPending, isFetching, refetch } = useTeams();
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
              <TableHead className="w-24">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((team) => (
              <TableRow key={team.name}>
                <TableCell className="font-medium">{team.name}</TableCell>
                <TableCell className="font-mono">{team.users}</TableCell>
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
            ))}
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
            <Skeleton className="h-4 w-16" />
          </div>
        ))}
      </div>
    </Card>
  );
}
