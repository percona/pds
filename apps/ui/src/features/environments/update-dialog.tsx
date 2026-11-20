import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert } from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { previewUpdate, startRun } from "./api";
import { environmentsKey } from "./environment-context";
import type { UpdateChange, UpdatePreview } from "./types";

type UpdateDialogProps = {
  environment: string;
  /** The stack's template, or a Kubernetes version the person picks. */
  kind: "template" | "kubernetes";
  currentVersion?: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

function replacementWords(change: UpdateChange): string {
  if (change.replacement === "True") {
    return "Replaced";
  }
  if (change.replacement === "Conditional") {
    return "May be replaced";
  }

  return "Kept";
}

/**
 * Shows what an update would change in the AWS account before anything runs. An
 * update that would replace the network or the Kubernetes cluster is never
 * started from here, and one that replaces the worker nodes needs a tick.
 */
export function UpdateDialog({
  environment,
  kind,
  currentVersion,
  open,
  onOpenChange,
}: UpdateDialogProps) {
  const queryClient = useQueryClient();
  const versionId = useId();
  const [version, setVersion] = useState("");
  const [replaceNodes, setReplaceNodes] = useState(false);

  const target = kind === "kubernetes" ? version.trim() : undefined;
  const preview = useMutation({
    mutationFn: () => previewUpdate(environment, target),
  });
  const start = useMutation({
    mutationFn: (replace: boolean) =>
      startRun(environment, {
        kind: "upgrade_kubernetes",
        ...(target ? { kubernetesVersion: target } : {}),
        ...(replace ? { confirmNodeGroupReplace: true } : {}),
      }),
    onSuccess: async () => {
      toast.success("Updating the environment");
      onOpenChange(false);
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
  });

  // A reopened dialog starts clean, and a template update previews at once.
  useEffect(() => {
    if (open) {
      setVersion("");
      setReplaceNodes(false);
      preview.reset();
      start.reset();
      if (kind === "template") {
        preview.mutate();
      }
    }
    // The mutations change identity on every render. Opening is what restarts the preview.
  }, [open, kind]);

  const result: UpdatePreview | undefined = preview.data;
  const blocked = result?.replacesNetwork === true;
  const needsConfirm = result?.replacesNodeGroup === true && !blocked;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {kind === "kubernetes" ? "Upgrade Kubernetes" : "Update the environment template"}
          </DialogTitle>
          <DialogDescription>
            {kind === "kubernetes"
              ? `Review what changes in AWS before the upgrade starts.${currentVersion ? ` This environment runs Kubernetes ${currentVersion}.` : ""}`
              : "Review what changes in AWS before the update starts."}
          </DialogDescription>
        </DialogHeader>

        {kind === "kubernetes" ? (
          <form
            className="space-y-3"
            noValidate
            onSubmit={(event) => {
              event.preventDefault();
              if (target) {
                setReplaceNodes(false);
                preview.mutate();
              }
            }}
          >
            <Field
              id={versionId}
              label="Kubernetes version"
              help="The minor version to upgrade to, for example 1.32."
            >
              {(props) => (
                <Input
                  {...props}
                  className="font-mono"
                  value={version}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => {
                    setVersion(event.target.value);
                    preview.reset();
                  }}
                />
              )}
            </Field>
            <Button
              type="submit"
              variant="outline"
              size="sm"
              disabled={!target}
              loading={preview.isPending}
            >
              {preview.isPending ? "Checking..." : "Preview changes"}
            </Button>
          </form>
        ) : null}

        {kind === "template" && preview.isPending ? <Skeleton className="h-20 w-full" /> : null}

        {preview.error ? <Problem>{preview.error.message}</Problem> : null}

        {blocked ? (
          <Problem>
            This update would replace the network or the Kubernetes cluster, so PDS does not start
            it. Contact Percona support.
          </Problem>
        ) : null}

        {result ? (
          <ul
            aria-label="Changes to the stack"
            className="max-h-60 space-y-2 overflow-y-auto rounded-lg border border-border px-4 py-3 text-sm"
          >
            {result.changes.length === 0 ? (
              <li className="text-muted-foreground">No resources change.</li>
            ) : (
              result.changes.map((change) => (
                <li
                  key={`${change.resource}-${change.action}`}
                  className="flex items-center justify-between gap-3"
                >
                  <span className="min-w-0 truncate font-mono">{change.resource}</span>
                  <span className="shrink-0 text-xs text-muted-foreground">
                    {change.action}. {replacementWords(change)}
                  </span>
                </li>
              ))
            )}
          </ul>
        ) : null}

        {needsConfirm ? (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              className="mt-0.5 size-4 accent-primary"
              checked={replaceNodes}
              onChange={(event) => setReplaceNodes(event.target.checked)}
            />
            <span>
              Replace the worker nodes. Pods move to new nodes, and databases restart one member at
              a time.
            </span>
          </label>
        ) : null}

        {start.error ? <Problem>{start.error.message}</Problem> : null}

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={start.isPending}
            onClick={() => onOpenChange(false)}
          >
            Cancel
          </Button>
          {result && !blocked ? (
            <Button
              type="button"
              disabled={(needsConfirm && !replaceNodes) || start.isPending}
              loading={start.isPending}
              onClick={() => start.mutate(needsConfirm && replaceNodes)}
            >
              {start.isPending ? "Updating..." : "Update"}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Problem({ children }: { children: string }) {
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
    >
      <CircleAlert
        className="mt-0.5 size-4 shrink-0 text-destructive"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <span>{children}</span>
    </div>
  );
}
