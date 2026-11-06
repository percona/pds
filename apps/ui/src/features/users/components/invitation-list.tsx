import { CircleAlert, Mail, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
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
import { useInvitations, useResendInvitation } from "../hooks/use-users";
import { roleLabel } from "../role-label";
import type { Invitation, IssuedInvitation } from "../types";
import { RevokeInvitationDialog } from "./revoke-invitation-dialog";

type InvitationListProps = {
  onIssued: (invitation: IssuedInvitation) => void;
};

/** What is still waiting to be claimed, with resend and revoke on each row. */
export function InvitationList({ onIssued }: InvitationListProps) {
  const { data, error, isPending, isFetching, refetch } = useInvitations();
  const resend = useResendInvitation();
  const [revoking, setRevoking] = useState<Invitation | null>(null);

  if (isPending) {
    return (
      <Card className="overflow-hidden" data-testid="invitation-list-loading">
        <div className="divide-y divide-border">
          {[0, 1].map((row) => (
            <div key={row} className="flex items-center gap-4 px-3 py-3">
              <Skeleton className="h-4 flex-1" />
              <Skeleton className="h-4 w-24" />
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
        title="Invites could not be read"
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
        icon={<Mail className="size-5 text-muted-foreground" strokeWidth={1.5} />}
        title="No pending invites"
        detail="Invites you send show here until they are claimed."
      />
    );
  }

  return (
    <>
      <Card className="overflow-hidden">
        <Table data-testid="invitation-list">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Invited</TableHead>
              <TableHead>Team</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead className="w-24">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((invitation) => {
              const who = invitation.email ?? "Anyone with the link";

              return (
                <TableRow key={invitation.id}>
                  <TableCell className="font-mono">{who}</TableCell>
                  <TableCell>{invitation.team}</TableCell>
                  <TableCell>{roleLabel(invitation.role)}</TableCell>
                  <TableCell className="font-mono">
                    {new Date(invitation.expiresAt).toLocaleDateString()}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`Resend ${who}`}
                        disabled={resend.isPending && resend.variables === invitation.id}
                        onClick={() =>
                          resend.mutate(invitation.id, {
                            onSuccess: onIssued,
                            onError: (failure) => toast.error(failure.message),
                          })
                        }
                      >
                        <RefreshCw className="size-4" strokeWidth={1.5} aria-hidden="true" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`Revoke ${who}`}
                        onClick={() => setRevoking(invitation)}
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

      <RevokeInvitationDialog
        invitation={revoking}
        onOpenChange={(open) => !open && setRevoking(null)}
      />
    </>
  );
}
