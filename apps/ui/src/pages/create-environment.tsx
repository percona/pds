import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, CircleAlert } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { toast } from "sonner";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  accountName,
  awsAccountsKey,
  ConnectAccountPanel,
  regionChoices,
  useAwsAccounts,
  type AwsAccount,
} from "@/features/aws-accounts";
import { StatusPill } from "@/features/databases";
import {
  createManagedEnvironment,
  environmentsKey,
  isEnvironmentName,
  reservedEnvironmentName,
  useEnvironments,
} from "@/features/environments";
import { useTeamToCreateIn } from "@/features/teams";
import { cn } from "@/lib/utils";
import { ConnectEnvironmentPage, CostNotice, ModeSwitch, Step } from "./connect-environment";

/**
 * Create an environment. PDS builds it in one of the Team's AWS accounts by
 * default, or the person runs a command themselves.
 */
export function CreateEnvironmentPage() {
  const [params] = useSearchParams();

  return params.get("mode") === "command" ? <ConnectEnvironmentPage /> : <ManagedCreate />;
}

const newAccount = "new";

function ManagedCreate() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [params] = useSearchParams();
  const { environments } = useEnvironments();
  const accounts = useAwsAccounts();
  const inTeam = useTeamToCreateIn();
  const [name, setName] = useState(() => params.get("name") ?? "");
  const [touched, setTouched] = useState(false);
  const [choice, setChoice] = useState<string | null>(params.get("account"));
  const [region, setRegion] = useState<string | null>(null);
  const regionId = useId();

  const rows = accounts.data ?? [];
  const chosen = rows.find((row) => row.id === choice);
  const usable = chosen?.state === "ready" ? chosen : undefined;
  const regions = regionChoices(usable?.allowedRegions ?? []);
  const selectedRegion =
    region !== null && regions.includes(region)
      ? region
      : regions.includes(usable?.stackRegion ?? "")
        ? (usable?.stackRegion ?? "")
        : (regions[0] ?? "");

  const taken = environments.some(
    (environment) =>
      environment.name === name && (inTeam.team === undefined || environment.team === inTeam.team),
  );
  const validName = isEnvironmentName(name) && !reservedEnvironmentName(name);
  const nameError = reservedEnvironmentName(name)
    ? "That name is taken by a page in this app. Pick another."
    : taken
      ? "An environment with that name already exists. Pick another."
      : touched && name.trim() !== "" && !isEnvironmentName(name)
        ? "Use lowercase letters, digits, and dashes, starting and ending with a letter or digit."
        : undefined;
  const ready = validName && !taken && usable !== undefined && selectedRegion !== "";

  const create = useMutation({
    mutationFn: () =>
      createManagedEnvironment(usable?.id ?? "", { name, region: selectedRegion }, inTeam.team),
    onSuccess: async () => {
      toast.success(`Creating ${name}`);
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
      // The environment's page follows the run phase by phase until it is ready.
      void navigate(`/environments/${encodeURIComponent(name)}`);
    },
  });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Create an environment"
        description="PDS builds an EKS cluster in your AWS account and connects it here as an environment."
      />

      <ModeSwitch mode="managed" name={name} />

      <div className="space-y-6">
        <Step n={1} title="Name this environment">
          <Field
            id="environment-name"
            label="Environment name"
            help="Lowercase letters, digits, and dashes."
            error={nameError}
          >
            {(props) => (
              <Input
                {...props}
                className="font-mono"
                value={name}
                autoComplete="off"
                onBlur={() => setTouched(true)}
                onChange={(event) => {
                  setName(event.target.value);
                  create.reset();
                }}
              />
            )}
          </Field>
          {inTeam.field}
        </Step>

        <Step n={2} title="Choose the AWS account">
          <AccountChoice
            accounts={rows}
            loading={accounts.isPending}
            error={accounts.error?.message}
            choice={choice}
            onChoose={(id) => {
              setChoice(id);
              setRegion(null);
              create.reset();
            }}
            onConnected={(account) => {
              void queryClient.invalidateQueries({ queryKey: awsAccountsKey });
              setChoice(account.id);
              setRegion(null);
            }}
            team={inTeam.team}
          />
        </Step>

        <Step n={3} title="Choose the region">
          {usable ? (
            <Field
              id={regionId}
              label="Region"
              help="The region the Kubernetes cluster is built in."
            >
              {(props) => (
                <NativeSelect
                  {...props}
                  className="font-mono"
                  value={selectedRegion}
                  onChange={(event) => setRegion(event.target.value)}
                >
                  {regions.map((value) => (
                    <option key={value} value={value}>
                      {value}
                    </option>
                  ))}
                </NativeSelect>
              )}
            </Field>
          ) : (
            <p className="text-sm text-muted-foreground">
              Choose a connected AWS account to pick a region.
            </p>
          )}
        </Step>

        <Step n={4} title="Review and create">
          {usable ? (
            <WhatPdsBuilds account={accountName(usable)} region={selectedRegion} />
          ) : (
            <p className="text-sm text-muted-foreground">
              Choose a connected AWS account to see what PDS will create.
            </p>
          )}
          <Button
            type="button"
            disabled={!ready || create.isPending}
            loading={create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? "Creating..." : "Create environment"}
          </Button>
          {create.error ? (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
            >
              <CircleAlert
                className="mt-0.5 size-4 shrink-0 text-destructive"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <span>{create.error.message}</span>
            </div>
          ) : null}
        </Step>
      </div>
    </div>
  );
}

/** What a create builds, in the words of the template it runs. */
const builtThings = [
  "A dedicated network: a VPC with public and private subnets, an internet gateway and a NAT gateway.",
  "An EKS cluster with a private endpoint, and a node group of worker nodes.",
  "The EBS CSI driver for storage, CoreDNS, and the Pod Identity agent.",
  "An S3 bucket for backups, and the IAM user the backups write as.",
  "PDS on the Kubernetes cluster, which then reports here as an environment.",
];

function WhatPdsBuilds({ account, region }: { account: string; region: string }) {
  return (
    <div className="space-y-3">
      <p className="text-sm">
        PDS will build this in AWS account <span className="font-mono">{account}</span>, region{" "}
        <span className="font-mono">{region}</span>:
      </p>
      <ul aria-label="What PDS builds" className="space-y-1.5 text-sm">
        {builtThings.map((thing) => (
          <li key={thing} className="flex items-start gap-2">
            <Check
              className="mt-0.5 size-3.5 shrink-0 text-muted-foreground"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <span>{thing}</span>
          </li>
        ))}
      </ul>
      <CostNotice>
        Everything is created in your AWS account, and AWS bills you for it until you delete the
        environment.
      </CostNotice>
    </div>
  );
}

type AccountChoiceProps = {
  accounts: AwsAccount[];
  loading: boolean;
  error: string | undefined;
  choice: string | null;
  onChoose: (id: string) => void;
  onConnected: (account: AwsAccount) => void;
  team: string | undefined;
};

/**
 * The Team's AWS accounts, with Connect another last. A Team with none sees the
 * connect command itself, and a broken account opens its fix where it is.
 */
function AccountChoice({
  accounts,
  loading,
  error,
  choice,
  onChoose,
  onConnected,
  team,
}: AccountChoiceProps) {
  const group = useId();

  // Nothing to choose from: the command is the step.
  const onlyCommand = !loading && !error && accounts.length === 0;
  useEffect(() => {
    if (onlyCommand && choice === null) {
      onChoose(newAccount);
    }
  }, [onlyCommand, choice, onChoose]);

  if (loading) {
    return <Skeleton className="h-16 w-full" />;
  }
  if (error) {
    return <p className="text-sm text-destructive">{error}</p>;
  }
  if (accounts.length === 0) {
    return <ConnectAccountPanel team={team} onConnected={onConnected} />;
  }

  const chosen = accounts.find((account) => account.id === choice);

  return (
    <div className="space-y-4">
      <div
        role="radiogroup"
        aria-label="AWS account"
        className="divide-y divide-border rounded-lg border border-border"
      >
        {accounts.map((account) => (
          <label
            key={account.id}
            className={cn(
              "flex cursor-pointer items-center justify-between gap-3 px-4 py-3 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-primary",
              choice === account.id && "bg-accent",
            )}
          >
            <span className="flex min-w-0 items-center gap-3">
              <input
                type="radio"
                name={group}
                className="size-4 accent-primary"
                checked={choice === account.id}
                onChange={() => onChoose(account.id)}
              />
              <span className="min-w-0">
                <span className="block truncate text-sm font-medium">{accountName(account)}</span>
                {account.alias ? (
                  <span className="block font-mono text-xs text-muted-foreground">
                    {account.accountId}
                  </span>
                ) : null}
              </span>
            </span>
            <AccountPill state={account.state} />
          </label>
        ))}
        <label
          className={cn(
            "flex cursor-pointer items-center gap-3 px-4 py-3 has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-primary",
            choice === newAccount && "bg-accent",
          )}
        >
          <input
            type="radio"
            name={group}
            className="size-4 accent-primary"
            checked={choice === newAccount}
            onChange={() => onChoose(newAccount)}
          />
          <span className="text-sm font-medium">Connect another AWS account</span>
        </label>
      </div>

      {choice === newAccount ? <ConnectAccountPanel team={team} onConnected={onConnected} /> : null}
      {chosen && chosen.state !== "ready" ? (
        <div className="space-y-4">
          <p className="text-sm">
            {chosen.reason ?? "PDS cannot use this AWS account until you run the command again."}
          </p>
          <ConnectAccountPanel team={team} onConnected={onConnected} />
        </div>
      ) : null}
    </div>
  );
}

function AccountPill({ state }: { state: AwsAccount["state"] }) {
  if (state === "ready") {
    return <StatusPill kind="ready" label="Connected" />;
  }
  if (state === "pending") {
    return <StatusPill kind="creating" label="Connecting" />;
  }

  return (
    <StatusPill kind="degraded" label={state === "out_of_date" ? "Out of date" : "Unreachable"} />
  );
}
