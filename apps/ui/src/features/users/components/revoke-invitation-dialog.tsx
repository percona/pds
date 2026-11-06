import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineError } from "@/components/ui/inline-error";
import { useRevokeInvitation } from "../hooks/use-users";
import type { Invitation } from "../types";

type RevokeInvitationDialogProps = {
  /** The invitation being revoked, or null while the dialog is closed. */
  invitation: Invitation | null;
  onOpenChange: (open: boolean) => void;
};

/** Revoking ends the link for good, so it is asked for once before it is done. */
export function RevokeInvitationDialog({ invitation, onOpenChange }: RevokeInvitationDialogProps) {
  const revoke = useRevokeInvitation();
  const who = invitation?.email ?? "anyone with the link";

  return (
    <Dialog
      open={invitation !== null}
      onOpenChange={(open) => {
        if (!open) {
          revoke.reset();
        }
        onOpenChange(open);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Revoke invite</DialogTitle>
          <DialogDescription className="text-foreground">
            The invite for <span className="font-mono">{who}</span> stops working. Send a new one to
            invite them again.
          </DialogDescription>
        </DialogHeader>

        <InlineError message={revoke.error?.message} />

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={revoke.isPending}
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          <Button
            variant="destructive"
            loading={revoke.isPending}
            onClick={() => {
              if (invitation === null) {
                return;
              }
              revoke.mutate(invitation.id, {
                onSuccess: () => {
                  toast.success("Invite revoked");
                  onOpenChange(false);
                },
              });
            }}
          >
            {revoke.isPending ? "Revoking..." : "Revoke"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
