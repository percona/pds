import { useMutation, useQueryClient } from "@tanstack/react-query";
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
import { isNameConfirmed } from "@/features/databases";
import { deleteManagedEnvironment } from "./api";
import { environmentsKey } from "./environment-context";

type ManagedDeleteDialogProps = {
  environment: string;
  /** The AWS account the environment lives in, by alias or ID. */
  account: string;
  /** Offers Remove from PDS only, for an AWS account PDS cannot use any more. */
  accountUnreachable?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

/**
 * Deleting a managed environment removes what PDS built in the AWS account, so
 * the dialog asks for the name typed out. Remove from PDS only is the way out
 * when the account cannot be reached: PDS forgets the environment and leaves
 * everything in the account, which keeps billing until the person removes it.
 */
export function ManagedDeleteDialog({
  environment,
  account,
  accountUnreachable = false,
  open,
  onOpenChange,
}: ManagedDeleteDialogProps) {
  const queryClient = useQueryClient();
  const fieldId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState("");
  const [touched, setTouched] = useState(false);

  const remove = useMutation({
    mutationFn: (pdsOnly: boolean) =>
      deleteManagedEnvironment(environment, typed.trim(), { pdsOnly }),
    onSuccess: async (_result, pdsOnly) => {
      toast.success(pdsOnly ? "Removed from PDS" : "Deleting the environment");
      onOpenChange(false);
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
  });

  // A reopened dialog must not arrive already confirmed or carry an old error.
  useEffect(() => {
    if (open) {
      setTyped("");
      setTouched(false);
      remove.reset();
    }
  }, [open, remove.reset]);

  const confirmed = isNameConfirmed(typed, environment);
  const mismatch = touched && typed.trim() !== "" && !confirmed;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-lg"
        onEscapeKeyDown={(event) => event.preventDefault()}
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          inputRef.current?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>Delete {environment}</DialogTitle>
          <DialogDescription data-testid="delete-consequence" className="text-foreground">
            This deletes <span className="font-mono">{environment}</span>, its databases, and the
            network and Kubernetes cluster PDS built for it in AWS account{" "}
            <span className="font-mono">{account}</span>, and there is no undo.
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            if (confirmed) {
              remove.mutate(false);
            }
          }}
        >
          <Field
            id={`${fieldId}-name`}
            label="Environment name"
            help={`Type ${environment} to confirm.`}
            error={mismatch ? "That is not this environment name." : undefined}
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

          {accountUnreachable ? (
            <p className="text-sm text-muted-foreground">
              PDS cannot reach this AWS account, so it cannot delete what it built there. Remove
              from PDS only forgets the environment and leaves everything in the account. It keeps
              billing until you delete it there.
            </p>
          ) : null}

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
            {accountUnreachable ? (
              <Button
                type="button"
                variant="outline"
                disabled={!confirmed || remove.isPending}
                onClick={() => remove.mutate(true)}
              >
                Remove from PDS only
              </Button>
            ) : null}
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
