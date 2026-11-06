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
import { useCreateTeam, useRenameTeam } from "../hooks/use-teams";

/** The width of the column the control plane keeps a Team's name in. */
const maxTeamNameLength = 128;

type TeamNameDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The Team being renamed, or undefined to create one. */
  team?: string;
};

/**
 * Creating a Team and renaming one ask for the same thing, a name, so they are
 * one dialog. A rename starts from the current name.
 */
export function TeamNameDialog({ open, onOpenChange, team }: TeamNameDialogProps) {
  const fieldId = useId();
  const [name, setName] = useState(team ?? "");
  const create = useCreateTeam();
  const rename = useRenameTeam(team ?? "");
  const creating = team === undefined;
  const write = creating ? create : rename;
  const title = creating ? "Create team" : `Rename ${team}`;
  const label = creating ? "Create team" : "Rename";
  const busyLabel = creating ? "Creating..." : "Renaming...";

  // A reopened dialog starts from the current name, with no error from the last try.
  useEffect(() => {
    if (open) {
      setName(team ?? "");
      create.reset();
      rename.reset();
    }
  }, [open, team, create.reset, rename.reset]);

  const trimmed = name.trim();
  const tooLong = trimmed.length > maxTeamNameLength;
  const ready = trimmed !== "" && !tooLong && trimmed !== team;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {creating
              ? "A team holds users and the environments they connect."
              : "Users and environments stay in the team."}
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            write.mutate(trimmed, {
              onSuccess: () => {
                toast.success(creating ? "Team created" : "Team renamed");
                onOpenChange(false);
              },
            });
          }}
        >
          <Field
            id={`${fieldId}-name`}
            label="Team name"
            help={`Up to ${maxTeamNameLength} characters. It is unique in your organization.`}
            error={tooLong ? `Use ${maxTeamNameLength} characters or fewer.` : undefined}
          >
            {(props) => (
              <Input
                {...props}
                value={name}
                onChange={(event) => setName(event.target.value)}
                autoComplete="off"
                autoFocus
                disabled={write.isPending}
              />
            )}
          </Field>

          <InlineError message={write.error?.message} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={write.isPending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!ready} loading={write.isPending}>
              {write.isPending ? busyLabel : label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
