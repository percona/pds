import { useEffect, useId, useState } from "react";
import { Link } from "react-router";
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
import { awsCleanupPath } from "./aws-cleanup";
import { openEverestPath } from "./openeverest";

type DisconnectDialogProps = {
  name: string;
  namespace?: string;
  open: boolean;
  pending: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
};

/** Asks for the environment name before it is removed from this control plane. */
export function DisconnectDialog({
  name,
  namespace,
  open,
  pending,
  onOpenChange,
  onConfirm,
}: DisconnectDialogProps) {
  const fieldId = useId();
  const [typed, setTyped] = useState("");

  useEffect(() => {
    if (open) {
      setTyped("");
    }
  }, [open]);

  const confirmed = isNameConfirmed(typed, name);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Disconnect {name}</DialogTitle>
          <DialogDescription className="text-foreground">
            This removes <span className="font-mono">{name}</span> from this control plane and
            revokes its token. Its saved configuration is kept. The Kubernetes cluster and its
            databases keep running. Nothing in AWS is deleted. If the join created an EKS cluster,
            its nodes and its backup bucket stay in your account and keep billing. One command
            deletes them.
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center gap-4">
          <Link
            to={awsCleanupPath(name, namespace)}
            className="rounded-md text-xs font-medium text-primary hover:underline"
          >
            View delete command
          </Link>
          <Link
            to={openEverestPath(name, namespace)}
            className="rounded-md text-xs font-medium text-primary hover:underline"
          >
            Continue with OpenEverest
          </Link>
        </div>
        <Field id={fieldId} label="Environment name" help="Type the environment name to confirm.">
          {(props) => (
            <Input
              {...props}
              className="font-mono"
              value={typed}
              autoComplete="off"
              onChange={(event) => setTyped(event.target.value)}
            />
          )}
        </Field>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            disabled={!confirmed}
            loading={pending}
            onClick={onConfirm}
          >
            Remove
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
