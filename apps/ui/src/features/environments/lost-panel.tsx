import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { formatTimestamp } from "@/lib/format";
import { startRun } from "./api";
import { environmentsKey } from "./environment-context";
import type { Lifecycle } from "./types";

type LostPanelProps = {
  environment: string;
  lifecycle: Lifecycle;
  /** Opens the Remove from PDS only dialog. */
  onRemove: () => void;
};

/**
 * An environment whose network and Kubernetes cluster are gone from AWS. PDS
 * cannot run anything on it, so the person rebuilds it or forgets it.
 */
export function LostPanel({ environment, lifecycle, onRemove }: LostPanelProps) {
  const queryClient = useQueryClient();
  const rebuild = useMutation({
    mutationFn: () => startRun(environment, { kind: "rebuild" }),
    onSuccess: async () => {
      toast.success("Rebuilding the environment");
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
    onError: (error: Error) => toast.error(error.message),
  });
  const lostAt = formatTimestamp(lifecycle.lostAt);

  return (
    <div className="space-y-3" data-testid="lost-panel">
      <div
        role="alert"
        className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
      >
        <CircleAlert
          className="mt-0.5 size-4 shrink-0 text-destructive"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className="font-medium">{lifecycle.lostReason ?? "Infrastructure missing"}</p>
          <p className="text-muted-foreground">
            PDS found no network or Kubernetes cluster for this environment in AWS
            {lostAt ? ` on ${lostAt}` : ""}. Its databases are not running.
          </p>
        </div>
      </div>
      <p className="text-sm text-muted-foreground">
        Rebuild creates a new network and Kubernetes cluster in the AWS account, which cost money.
        Remove from PDS only forgets the environment and deletes nothing in AWS.
      </p>
      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          loading={rebuild.isPending}
          onClick={() => rebuild.mutate()}
        >
          {rebuild.isPending ? "Rebuilding..." : "Rebuild"}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onRemove}>
          Remove from PDS only
        </Button>
      </div>
    </div>
  );
}
