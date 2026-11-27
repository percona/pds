import { ArrowRightLeft, CircleAlert, Shield, UserMinus, Users } from "lucide-react";
import { useState, type ReactNode } from "react";
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
import { useMembers } from "../hooks/use-users";
import { roleLabel } from "../role-label";
import type { Member } from "../types";
import { ChangeRoleDialog, MoveMemberDialog, RemoveMemberDialog } from "./member-dialogs";

type MemberListProps = {
  /** Shows only this Team's Users, without a Team column. Every User when left out. */
  team?: string;
  /** What an empty Team shows instead of the default empty state. */
  empty?: ReactNode;
};

type Action = { kind: "move" | "role" | "remove"; member: Member };

/**
 * The Users of the Organization, with the Team and role each holds, and the
 * three things an admin does to a User.
 */
export function MemberList({ team, empty }: MemberListProps) {
  const { data, error, isPending, isFetching, refetch } = useMembers();
  const [action, setAction] = useState<Action | null>(null);

  if (isPending) {
    return (
      <Card className="overflow-hidden" data-testid="member-list-loading">
        <div className="divide-y divide-border">
          {[0, 1, 2].map((row) => (
            <div key={row} className="flex items-center gap-4 px-3 py-3">
              <Skeleton className="h-4 flex-1" />
              <Skeleton className="h-4 w-32" />
              <Skeleton className="h-4 w-16" />
            </div>
          ))}
        </div>
      </Card>
    );
  }

  if (error) {
    return (
      <Notice
        icon={<CircleAlert className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="Users could not be read"
        detail={error.message}
        action={
          <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
            Try again
          </Button>
        }
      />
    );
  }

  const members = team === undefined ? data : data.filter((member) => member.team === team);

  if (members.length === 0) {
    return (
      empty ?? (
        <Notice
          icon={<Users className="size-5 text-muted-foreground" strokeWidth={1.5} />}
          title="No users yet"
          detail="Users show here once an invite is claimed."
        />
      )
    );
  }

  const close = (open: boolean) => {
    if (!open) {
      setAction(null);
    }
  };

  return (
    <>
      <Card className="overflow-hidden">
        <Table data-testid="member-list">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>User</TableHead>
              <TableHead>Email</TableHead>
              {team === undefined ? <TableHead>Team</TableHead> : null}
              <TableHead>Role</TableHead>
              <TableHead className="w-32">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {members.map((member) => (
              <TableRow key={member.id}>
                <TableCell className="font-medium">{member.name}</TableCell>
                <TableCell className="font-mono">{member.email ?? ""}</TableCell>
                {team === undefined ? <TableCell>{member.team}</TableCell> : null}
                <TableCell>{roleLabel(member.role)}</TableCell>
                <TableCell>
                  <div className="flex justify-end gap-1">
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Move ${member.name} to another team`}
                      onClick={() => setAction({ kind: "move", member })}
                    >
                      <ArrowRightLeft className="size-4" strokeWidth={1.5} aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Change the role of ${member.name}`}
                      onClick={() => setAction({ kind: "role", member })}
                    >
                      <Shield className="size-4" strokeWidth={1.5} aria-hidden="true" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Remove ${member.name}`}
                      onClick={() => setAction({ kind: "remove", member })}
                    >
                      <UserMinus className="size-4" strokeWidth={1.5} aria-hidden="true" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Card>

      {action?.kind === "move" ? (
        <MoveMemberDialog member={action.member} open onOpenChange={close} />
      ) : null}
      {action?.kind === "role" ? (
        <ChangeRoleDialog member={action.member} open onOpenChange={close} />
      ) : null}
      {action?.kind === "remove" ? (
        <RemoveMemberDialog member={action.member} open onOpenChange={close} />
      ) : null}
    </>
  );
}
