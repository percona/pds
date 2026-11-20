import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
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
import { Notice } from "@/components/ui/notice";
import { Skeleton } from "@/components/ui/skeleton";
import { isNameConfirmed, StatusPill, type StatusKind } from "@/features/databases";
import { removeAwsAccount } from "../api";
import { awsAccountsKey, useAwsAccounts } from "../hooks";
import type { AwsAccount, AwsAccountState } from "../types";
import { ConnectAccountPanel } from "./connect-account-panel";

export const connectAccountPath = "/environments/aws-accounts/new";

/** What a person calls an account: its alias, or its ID when it has none. */
export function accountName(account: Pick<AwsAccount, "alias" | "accountId">): string {
  return account.alias ? account.alias : account.accountId;
}

const pills: Record<AwsAccountState, { kind: StatusKind; label: string }> = {
  ready: { kind: "ready", label: "Connected" },
  pending: { kind: "creating", label: "Connecting" },
  unreachable: { kind: "degraded", label: "Unreachable" },
  out_of_date: { kind: "degraded", label: "Out of date" },
};

/**
 * The AWS accounts the Team connected, with Fix for one that stopped working,
 * Details and Remove. It renders nothing when this control plane cannot connect
 * accounts, so the page is the one it was before.
 */
export function AwsAccountsSection() {
  const accounts = useAwsAccounts();
  const [params] = useSearchParams();
  const fixing = params.get("fix");

  if (accounts.isPending) {
    return (
      <section className="space-y-3" aria-label="AWS accounts" data-testid="aws-accounts-loading">
        <Skeleton className="h-6 w-32" />
        <Skeleton className="h-16 w-full" />
      </section>
    );
  }
  if (accounts.error) {
    return null;
  }
  const rows = accounts.data ?? [];

  return (
    <section id="aws-accounts" className="space-y-3">
      <div className="flex items-center justify-between gap-4">
        <h2 className="text-base font-semibold">AWS accounts</h2>
        {rows.length > 0 ? (
          <Button asChild variant="outline" size="sm">
            <Link to={connectAccountPath}>Connect an AWS account</Link>
          </Button>
        ) : null}
      </div>
      {rows.length === 0 ? (
        <Notice
          icon={null}
          title="No AWS accounts yet"
          detail="Connect an AWS account and PDS can create environments in it for you."
          action={
            <Button asChild>
              <Link to={connectAccountPath}>Connect an AWS account</Link>
            </Button>
          }
        />
      ) : (
        <Card className="overflow-hidden">
          <ul className="divide-y divide-border">
            {rows.map((account) => (
              <AccountRow key={account.id} account={account} fixOpen={fixing === account.id} />
            ))}
          </ul>
        </Card>
      )}
    </section>
  );
}

type Panel = "fix" | "details" | null;

function AccountRow({ account, fixOpen }: { account: AwsAccount; fixOpen: boolean }) {
  const [panel, setPanel] = useState<Panel>(fixOpen ? "fix" : null);
  const [removing, setRemoving] = useState(false);
  const pill = pills[account.state];
  const broken = account.state === "unreachable" || account.state === "out_of_date";

  useEffect(() => {
    if (fixOpen) {
      setPanel("fix");
    }
  }, [fixOpen]);

  function toggle(next: Exclude<Panel, null>) {
    setPanel((current) => (current === next ? null : next));
  }

  return (
    <li>
      <div className="flex flex-wrap items-center justify-between gap-3 px-5 py-4">
        <div className="min-w-0 space-y-1">
          <p className="truncate text-sm font-medium">{accountName(account)}</p>
          {account.alias ? (
            <p className="font-mono text-xs text-muted-foreground">{account.accountId}</p>
          ) : null}
        </div>
        <StatusPill kind={pill.kind} label={pill.label} />
        <div className="flex items-center gap-1">
          {broken ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-expanded={panel === "fix"}
              onClick={() => toggle("fix")}
            >
              Fix
            </Button>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="sm"
            aria-expanded={panel === "details"}
            onClick={() => toggle("details")}
          >
            Details
          </Button>
          <Button type="button" variant="ghost" size="sm" onClick={() => setRemoving(true)}>
            Remove
          </Button>
        </div>
      </div>
      {panel === "fix" ? (
        <div className="space-y-4 border-t border-border px-5 py-5">
          <p className="text-sm">
            {account.reason ??
              (account.state === "out_of_date"
                ? "The roles in this account use an older template."
                : "PDS cannot use this AWS account.")}
          </p>
          <p className="text-sm text-muted-foreground">
            Run the command again to repair it. The roles come back as they were, so environments in
            this account need nothing else.
          </p>
          <ConnectAccountPanel />
        </div>
      ) : null}
      {panel === "details" ? <AccountDetails account={account} /> : null}
      <RemoveAccountDialog account={account} open={removing} onOpenChange={setRemoving} />
    </li>
  );
}

function AccountDetails({ account }: { account: AwsAccount }) {
  return (
    <dl className="grid gap-x-6 gap-y-2 border-t border-border px-5 py-5 text-sm sm:grid-cols-[max-content_1fr]">
      <dt className="text-muted-foreground">Account ID</dt>
      <dd className="font-mono">{account.accountId}</dd>
      <dt className="text-muted-foreground">Regions</dt>
      <dd className="font-mono">{account.allowedRegions.join(", ")}</dd>
      <dt className="text-muted-foreground">Environments</dt>
      <dd>
        {account.environments.length === 0
          ? "None yet"
          : account.environments.map((name, index) => (
              <span key={name}>
                {index > 0 ? ", " : ""}
                <span className="font-mono">{name}</span>
              </span>
            ))}
      </dd>
    </dl>
  );
}

function RemoveAccountDialog({
  account,
  open,
  onOpenChange,
}: {
  account: AwsAccount;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const fieldId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [typed, setTyped] = useState("");
  const name = accountName(account);
  const remove = useMutation({
    mutationFn: () => removeAwsAccount(account.id, typed.trim()),
    onSuccess: async () => {
      toast.success("AWS account removed");
      onOpenChange(false);
      await queryClient.invalidateQueries({ queryKey: awsAccountsKey });
    },
  });

  useEffect(() => {
    if (open) {
      setTyped("");
      remove.reset();
    }
  }, [open, remove.reset]);

  const confirmed = isNameConfirmed(typed, name);

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
          <DialogTitle>Remove {name}</DialogTitle>
          <DialogDescription className="text-foreground">
            PDS forgets this AWS account. The roles stay in the account, and you delete them there.
            An account with environments cannot be removed until they are deleted.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            if (confirmed) {
              remove.mutate();
            }
          }}
        >
          <Field id={`${fieldId}-name`} label="Account name" help={`Type ${name} to confirm.`}>
            {(props) => (
              <Input
                {...props}
                ref={inputRef}
                value={typed}
                onChange={(event) => setTyped(event.target.value)}
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
              {remove.isPending ? "Removing..." : "Remove AWS account"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
