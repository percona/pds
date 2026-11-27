import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, CircleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
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
import { Skeleton } from "@/components/ui/skeleton";
import { isNameConfirmed } from "@/features/databases";
import { deleteManagedEnvironment, fetchRemains } from "./api";
import { environmentsKey } from "./environment-context";
import type { Remains } from "./types";

type ManagedDeleteDialogProps = {
  environment: string;
  /** The AWS account the environment lives in, by alias or ID. */
  account: string;
  /** Offers Remove from PDS only, for an AWS account PDS cannot use any more. */
  accountUnreachable?: boolean;
  /** The infrastructure is gone, so the only way out is Remove from PDS only. */
  lost?: boolean;
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
  lost = false,
  open,
  onOpenChange,
}: ManagedDeleteDialogProps) {
  const queryClient = useQueryClient();
  const fieldId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState("");
  const [touched, setTouched] = useState(false);

  const pdsOnlyOffered = accountUnreachable || lost;
  const remains = useQuery({
    queryKey: [...environmentsKey, "remains", environment],
    queryFn: ({ signal }) => fetchRemains(environment, signal),
    enabled: open && pdsOnlyOffered,
  });

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
          <DialogTitle>
            {lost ? `Remove ${environment} from PDS` : `Delete ${environment}`}
          </DialogTitle>
          <DialogDescription data-testid="delete-consequence" className="text-foreground">
            {lost ? (
              <>
                This forgets <span className="font-mono">{environment}</span> in PDS. Its network
                and Kubernetes cluster are already missing from AWS account{" "}
                <span className="font-mono">{account}</span>, and what is left there stays there.
              </>
            ) : (
              <>
                This deletes <span className="font-mono">{environment}</span>, its databases, and
                the network and Kubernetes cluster PDS built for it in AWS account{" "}
                <span className="font-mono">{account}</span>, and there is no undo.
              </>
            )}
          </DialogDescription>
        </DialogHeader>

        {lost ? null : <WhatDeletingRemoves />}

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

          {pdsOnlyOffered ? (
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">
                {lost
                  ? "Remove from PDS only forgets the environment and deletes nothing in AWS."
                  : "PDS cannot reach this AWS account, so it cannot delete what it built there. Remove from PDS only forgets the environment and leaves everything in the account."}{" "}
                What stays in AWS keeps billing until you delete it there.
              </p>
              <WhatStays remains={remains.data} failed={remains.isError} />
            </div>
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
            {accountUnreachable && !lost ? (
              <Button
                type="button"
                variant="outline"
                disabled={!confirmed || remove.isPending}
                onClick={() => remove.mutate(true)}
              >
                Remove from PDS only
              </Button>
            ) : null}
            {lost ? (
              <Button
                type="button"
                variant="destructive"
                disabled={!confirmed}
                loading={remove.isPending}
                onClick={() => remove.mutate(true)}
              >
                Remove from PDS only
              </Button>
            ) : (
              <Button
                type="submit"
                variant="destructive"
                disabled={!confirmed}
                loading={remove.isPending}
              >
                {remove.isPending ? "Deleting..." : "Delete"}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** The delete run's steps, said as what is gone once it finishes. */
const deletedThings: ReactNode[] = [
  "The databases and their data",
  "The load balancers and volumes",
  "The network, the Kubernetes cluster and its worker nodes",
  <>
    Anything AWS left behind that carries the <span className="font-mono">percona.com/pds</span> tag
  </>,
  "The backup bucket and every backup in it",
  "The IAM user the backups wrote as. If the environment cannot remove it, PDS names it and you delete it in the IAM console",
];

function WhatDeletingRemoves() {
  return (
    <div className="space-y-2">
      <p className="text-sm font-medium">What deleting removes</p>
      <ul aria-label="What deleting removes" className="space-y-1.5 text-sm">
        {deletedThings.map((thing, index) => (
          <li key={index} className="flex items-start gap-2">
            <Check
              className="mt-0.5 size-3.5 shrink-0 text-muted-foreground"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <span>{thing}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** What a PDS only removal leaves in the AWS account, from the control plane's own record. */
function WhatStays({ remains, failed }: { remains: Remains | undefined; failed: boolean }) {
  if (failed) {
    return (
      <p className="text-sm text-muted-foreground">
        The list of what stays in AWS could not be read. Check the AWS account for resources tagged
        <span className="font-mono"> percona.com/pds</span>.
      </p>
    );
  }
  if (!remains) {
    return <Skeleton className="h-16 w-full" />;
  }

  return (
    <ul
      aria-label="What stays in AWS"
      className="space-y-1 rounded-lg border border-border px-4 py-3 text-sm"
    >
      <li>
        CloudFormation stack <span className="font-mono">{remains.stackName}</span>
      </li>
      <li>
        EKS cluster <span className="font-mono">{remains.eksName}</span>
      </li>
      {remains.bucket ? (
        <li>
          Backup bucket <span className="font-mono">{remains.bucket}</span>
        </li>
      ) : null}
      <li>
        Region <span className="font-mono">{remains.region}</span>
      </li>
    </ul>
  );
}
