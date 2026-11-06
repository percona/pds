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
import { useRevokeAgentSession } from "../hooks/use-agent-sessions";
import type { AgentSession } from "../types";

type RevokeAgentDialogProps = {
  /** The agent being revoked, or null while the dialog is closed. */
  agent: AgentSession | null;
  onOpenChange: (open: boolean) => void;
};

/** Revoking ends the sign in for good, so it is asked for once before it is done. */
export function RevokeAgentDialog({ agent, onOpenChange }: RevokeAgentDialogProps) {
  const revoke = useRevokeAgentSession();
  const name = agent?.name ?? "The agent";
  const owner = agent && !agent.mine ? ` for ${agent.member}` : "";

  return (
    <Dialog
      open={agent !== null}
      onOpenChange={(open) => {
        if (!open) {
          revoke.reset();
        }
        onOpenChange(open);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Revoke agent</DialogTitle>
          <DialogDescription className="text-foreground">
            {`${name}${owner} stops working at once and has to sign in again.`}
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
              if (agent === null) {
                return;
              }
              revoke.mutate(agent.id, {
                onSuccess: () => {
                  toast.success("Agent revoked");
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
