import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { InviteOutcome } from "../types";
import { InviteForm } from "./invite-form";

type InviteDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The Team the form starts on. */
  team?: string;
  onSent: (outcomes: InviteOutcome[]) => void;
};

/** Invite people, from the Users page or from a Team's own page. */
export function InviteDialog({ open, onOpenChange, team, onSent }: InviteDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-4rem)] max-w-lg overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Invite people</DialogTitle>
          <DialogDescription>
            Each address gets its own invite and link. A link works once, and it is shown once,
            after it is sent.
          </DialogDescription>
        </DialogHeader>
        <InviteForm
          team={team}
          onSent={(outcomes) => {
            onSent(outcomes);
            onOpenChange(false);
          }}
        />
      </DialogContent>
    </Dialog>
  );
}
