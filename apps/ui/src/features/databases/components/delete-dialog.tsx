import { CircleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
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
import { Input } from "@/components/ui/input";
import { isNameConfirmed, MONGODB_BACKUPS_DELETED, POSTGRESQL_BACKUPS_STAY } from "../confirm";
import { useDeleteDatabase } from "../hooks/use-delete-database";
import type { Database } from "../types";

type DeleteDialogProps = {
  database: Database;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called once the adapter has accepted the delete, so the page can leave. */
  onDeleted?: () => void;
};

/**
 * The delete dialog. Delete is the one action with nothing behind it afterwards,
 * so it states the consequence in one sentence and asks for the database name
 * typed out: a confirmation you can click through is not a confirmation.
 *
 * The field is plain state rather than a form library, as in the restore dialog.
 * There is one input and one rule, and the rule is the same string comparison
 * the button reads.
 */
export function DeleteDialog({ database, open, onOpenChange, onDeleted }: DeleteDialogProps) {
  const fieldId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState("");
  const [touched, setTouched] = useState(false);
  const remove = useDeleteDatabase(database.id);

  // A reopened dialog must not arrive already confirmed, and a refused attempt
  // must not leave its error over a fresh one.
  useEffect(() => {
    if (open) {
      setTyped("");
      setTouched(false);
      remove.reset();
    }
  }, [open, remove.reset]);

  const confirmed = isNameConfirmed(typed, database.name);
  const mismatch = touched && typed.trim() !== "" && !confirmed;

  const submit = () => {
    remove.mutate(undefined, {
      onSuccess: () => {
        toast.success("Database deleted");
        onOpenChange(false);
        onDeleted?.();
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-lg"
        // Esc is kept for the dialogs that change nothing. Leaving this one
        // is a decision, so Cancel is how it is made.
        onEscapeKeyDown={(event) => event.preventDefault()}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          inputRef.current?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>Delete {database.name}</DialogTitle>
          <DialogDescription data-testid="delete-consequence" className="text-foreground">
            This deletes <span className="font-mono">{database.name}</span> with its data
            {database.engine === "postgresql" ? "" : " and its backups"}, and there is no undo.
            {database.engine === "postgresql" ? ` ${POSTGRESQL_BACKUPS_STAY}` : ""}
            {database.engine === "mongodb" ? ` ${MONGODB_BACKUPS_DELETED}` : ""}
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <Field
            id={`${fieldId}-name`}
            label="Database name"
            help={`Type ${database.name} to confirm.`}
            error={mismatch ? "That is not this database name." : undefined}
          >
            {(props) => (
              <Input
                {...props}
                ref={inputRef}
                value={typed}
                onChange={(event) => setTyped(event.target.value)}
                onBlur={() => setTouched(true)}
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
                disabled={remove.isPending}
              />
            )}
          </Field>

          {remove.error ? (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
            >
              <CircleAlert
                className="mt-0.5 size-4 shrink-0 text-destructive"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <span>{remove.error.message}</span>
            </div>
          ) : null}

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
