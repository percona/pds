import { useEffect, useId, useState } from "react";
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
import { Field } from "@/components/ui/field";
import { InlineError } from "@/components/ui/inline-error";
import { Input } from "@/components/ui/input";
import { useDeleteTeam } from "../hooks/use-teams";

type DeleteTeamDialogProps = {
  team: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

/**
 * Deleting a Team asks for its name typed out. The control plane refuses a
 * Team that still has users or environments and says which, and that sentence
 * is shown as it stands.
 */
export function DeleteTeamDialog({ team, open, onOpenChange }: DeleteTeamDialogProps) {
  const fieldId = useId();
  const [typed, setTyped] = useState("");
  const remove = useDeleteTeam(team);

  useEffect(() => {
    if (open) {
      setTyped("");
      remove.reset();
    }
  }, [open, remove.reset]);

  const confirmed = typed.trim() === team;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        // Esc is kept for the dialogs that change nothing. Leaving this one
        // is a decision, so Cancel is how it is made.
        onEscapeKeyDown={(event) => event.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>Delete {team}</DialogTitle>
          <DialogDescription className="text-foreground">
            This deletes the team <span className="font-mono">{team}</span>. It works only when the
            team has no users and no environments.
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            remove.mutate(undefined, {
              onSuccess: () => {
                toast.success("Team deleted");
                onOpenChange(false);
              },
            });
          }}
        >
          <Field id={`${fieldId}-name`} label="Team name" help={`Type ${team} to confirm.`}>
            {(props) => (
              <Input
                {...props}
                value={typed}
                onChange={(event) => setTyped(event.target.value)}
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                disabled={remove.isPending}
              />
            )}
          </Field>

          <InlineError message={remove.error?.message} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={remove.isPending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant="destructive"
              disabled={!confirmed}
              loading={remove.isPending}
            >
              {remove.isPending ? "Deleting..." : "Delete"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
