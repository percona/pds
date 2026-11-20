import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, CircleCheck, CircleDashed, CircleX, Loader } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { retryRun } from "./api";
import { environmentsKey } from "./environment-context";
import type { PhaseStatus, Provisioning } from "./types";

const phaseWords: Record<PhaseStatus, string> = {
  pending: "Waiting",
  running: "Running",
  waiting: "Paused",
  succeeded: "Done",
  failed: "Failed",
};

function PhaseIcon({ status }: { status: PhaseStatus }) {
  if (status === "succeeded") {
    return <CircleCheck className="size-4 text-success" strokeWidth={1.5} aria-hidden="true" />;
  }
  if (status === "failed") {
    return <CircleX className="size-4 text-destructive" strokeWidth={1.5} aria-hidden="true" />;
  }
  if (status === "running") {
    return (
      <Loader
        className="size-4 animate-soft-pulse text-muted-foreground"
        strokeWidth={1.5}
        aria-hidden="true"
      />
    );
  }
  if (status === "waiting") {
    return <CircleAlert className="size-4 text-warning" strokeWidth={1.5} aria-hidden="true" />;
  }

  return (
    <CircleDashed className="size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
  );
}

type ProvisioningPanelProps = {
  environment: string;
  provisioning: Provisioning;
};

/**
 * How the latest run of a managed environment stands: the four phases of a
 * create, why it stopped when it did, and Retry, which re-runs only the step
 * that failed.
 */
export function ProvisioningPanel({ environment, provisioning }: ProvisioningPanelProps) {
  const queryClient = useQueryClient();
  const retry = useMutation({
    mutationFn: () => retryRun(environment, provisioning.run.id),
    onSuccess: async () => {
      toast.success("Retrying the step that failed");
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
    onError: (error: Error) => toast.error(error.message),
  });

  const deleting = provisioning.run.kind === "delete";
  const failure = provisioning.failure;
  const stopped = provisioning.progress === "failed" || provisioning.progress === "blocked";

  return (
    <div className="space-y-3" data-testid="provisioning-panel">
      {provisioning.phases.length > 0 ? (
        <ol className="space-y-2" aria-label="Create progress">
          {provisioning.phases.map((phase) => (
            <li key={phase.name} className="flex items-center gap-2 text-sm">
              <PhaseIcon status={phase.status} />
              <span>{phase.name}</span>
              <span className="text-xs text-muted-foreground">{phaseWords[phase.status]}</span>
            </li>
          ))}
        </ol>
      ) : null}

      {deleting && !stopped && provisioning.progress !== "ready" ? (
        <p className="text-sm text-muted-foreground">
          Deleting the environment and what PDS built in AWS.
        </p>
      ) : null}

      {provisioning.message && !stopped ? (
        <p className="text-sm text-muted-foreground">{provisioning.message}</p>
      ) : null}

      {stopped && failure ? (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
        >
          <CircleAlert
            className="mt-0.5 size-4 shrink-0 text-destructive"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <span>{failure.sentence}</span>
        </div>
      ) : null}

      {stopped && failure?.action === "retry" ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          loading={retry.isPending}
          onClick={() => retry.mutate()}
        >
          {retry.isPending ? "Retrying..." : "Retry"}
        </Button>
      ) : null}
    </div>
  );
}
