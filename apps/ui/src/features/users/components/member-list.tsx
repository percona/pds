import { CircleAlert, Users } from "lucide-react";
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

/** The Users of the Organization, with the Team and role each holds. */
export function MemberList() {
  const { data, error, isPending, isFetching, refetch } = useMembers();

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

  if (data.length === 0) {
    return (
      <Notice
        icon={<Users className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="No users yet"
        detail="Users show here once an invite is claimed."
      />
    );
  }

  return (
    <Card className="overflow-hidden">
      <Table data-testid="member-list">
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead>User</TableHead>
            <TableHead>Email</TableHead>
            <TableHead>Team</TableHead>
            <TableHead>Role</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data.map((member) => (
            <TableRow key={member.id}>
              <TableCell className="font-medium">{member.name}</TableCell>
              <TableCell className="font-mono">{member.email ?? ""}</TableCell>
              <TableCell>{member.team}</TableCell>
              <TableCell>{roleLabel(member.role)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
