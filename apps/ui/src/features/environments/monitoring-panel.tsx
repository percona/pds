import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleDashed, Copy, TriangleAlert } from "lucide-react";
import { useState, type FormEvent } from "react";
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
import { allowsAnyAddress, parseSourceRanges } from "@/features/databases/access";
import { cn } from "@/lib/utils";
import { fetchMonitoring, fetchMonitoringPassword, putMonitoring } from "./api";
import { CommandBlock } from "./command-block";
import { copyResolved } from "./copy-resolved";
import type { Monitoring, MonitoringAccessChoice, MonitoringSettings } from "./types";

const accessChoices: { value: MonitoringAccessChoice; label: string }[] = [
  { value: "cluster", label: "Inside the Kubernetes cluster" },
  { value: "private", label: "Private" },
  { value: "public", label: "Public" },
];

const accessSummary: Record<MonitoringAccessChoice, { description: string }> = {
  cluster: { description: "Reachable only from inside the Kubernetes cluster." },
  private: { description: "Reachable from your VPC and the networks connected to it." },
  public: { description: "Reachable from the internet." },
};

/** How often the panel looks again while the cloud is still assigning an address. */
const addressPollMs = 15_000;

function monitoringKey(environment: string) {
  return ["environments", environment, "monitoring"] as const;
}

/** What a change of access does, in the one sentence the confirmation shows. */
function changeConsequence(from: MonitoringAccessChoice, to: MonitoringAccessChoice): string {
  if (to === "cluster") {
    return "This removes the load balancer. Monitoring is then reachable only from inside the Kubernetes cluster, and its current address stops working.";
  }
  if (from === "cluster") {
    return `This adds a load balancer in your cloud account, billed by your cloud provider. Monitoring becomes ${
      to === "public" ? "reachable from the internet" : "reachable from your VPC"
    } at the allowed addresses.`;
  }

  return "This replaces the load balancer, so the address changes and the current address stops working.";
}

/**
 * An environment's monitoring: how to sign in, how far it is reachable, and the
 * settings the environment applies on its own once they are saved. The password is
 * never shown. Copy password asks the environment for it on click and puts it on
 * the clipboard, so signing in needs no environment access. The kubectl commands
 * stay under Advanced for anyone who has it.
 */
export function MonitoringPanel({ environment }: { environment: string }) {
  const query = useQuery({
    queryKey: monitoringKey(environment),
    queryFn: ({ signal }) => fetchMonitoring(environment, signal),
    // An exposed service has no address until the cloud assigns one, and the
    // environment reports it on its next beat. Look again until it does.
    refetchInterval: (current) => {
      const data = current.state.data;

      return data && data.settings.access !== "cluster" && !data.access.address
        ? addressPollMs
        : false;
    },
  });

  if (query.isPending) {
    return (
      <div data-testid="monitoring-loading" className="space-y-3">
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-9 w-full" />
      </div>
    );
  }
  if (query.isError) {
    return (
      <p role="alert" className="text-sm text-destructive">
        Monitoring could not be read. {query.error.message}
      </p>
    );
  }

  return <MonitoringForm environment={environment} monitoring={query.data} />;
}

function MonitoringForm({
  environment,
  monitoring,
}: {
  environment: string;
  monitoring: Monitoring;
}) {
  const queryClient = useQueryClient();
  const { settings, access: login } = monitoring;
  const [access, setAccess] = useState<MonitoringAccessChoice>(settings.access);
  const [ranges, setRanges] = useState((settings.sourceRanges ?? []).join(", "));
  const [rangesError, setRangesError] = useState<string>();
  const [cpu, setCpu] = useState(String(settings.cpuMillicores));
  const [memory, setMemory] = useState(String(settings.memoryMi));
  const [storage, setStorage] = useState(String(settings.storageGi));
  const [copying, setCopying] = useState(false);
  // The settings waiting on a yes, when the change would add, replace or
  // remove a load balancer.
  const [confirming, setConfirming] = useState<MonitoringSettings>();

  const save = useMutation({
    mutationFn: (next: MonitoringSettings) => putMonitoring(environment, next),
    onSuccess: async (saved) => {
      queryClient.setQueryData(monitoringKey(environment), saved);
      toast.success("Monitoring settings saved");
    },
    onSettled: () => setConfirming(undefined),
  });

  const parsed = parseSourceRanges(ranges);
  const openToAnyAddress = access !== "cluster" && allowsAnyAddress(parsed.ranges);
  const exposedNow = settings.access !== "cluster";

  async function copyPassword(): Promise<void> {
    setCopying(true);
    try {
      await copyResolved(async () => {
        const password = await fetchMonitoringPassword(environment);
        if (!password) {
          throw new Error("The environment returned no password.");
        }

        return password;
      });
      toast.success("Password copied");
    } catch (error) {
      if (error instanceof Error && error.name === "NotAllowedError") {
        toast.error("The browser blocked the copy. Try again.");

        return;
      }
      toast.error(error instanceof Error ? error.message : "The password could not be copied.");
    } finally {
      setCopying(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();

    if (access !== "cluster") {
      if (parsed.error) {
        setRangesError(parsed.error);

        return;
      }
      if (access === "public" && parsed.ranges.length === 0) {
        setRangesError("Enter at least one address or range.");

        return;
      }
    }
    setRangesError(undefined);

    const next: MonitoringSettings = {
      access,
      // A private service with no ranges is limited to the private address
      // ranges by the control plane, so there is nothing to send.
      ...(access !== "cluster" && parsed.ranges.length > 0 ? { sourceRanges: parsed.ranges } : {}),
      cpuMillicores: Number(cpu),
      memoryMi: Number(memory),
      storageGi: Number(storage),
    };

    if (access !== settings.access) {
      setConfirming(next);

      return;
    }
    save.mutate(next);
  }

  return (
    <div className="space-y-6">
      <section className="space-y-3" aria-labelledby={`monitoring-access-${environment}`}>
        <h3 id={`monitoring-access-${environment}`} className="text-base font-semibold">
          Sign in to monitoring
        </h3>
        {exposedNow && login.address ? (
          <p className="text-sm text-muted-foreground">
            Sign in as <span className="font-mono">{login.username}</span> at{" "}
            <a
              href={login.url}
              target="_blank"
              rel="noreferrer noopener"
              className="font-mono text-primary underline underline-offset-2"
            >
              {login.url}
            </a>
            . Your browser warns about the certificate, because monitoring uses a self-signed
            certificate.
          </p>
        ) : exposedNow ? (
          <p className="flex items-start gap-2 text-sm">
            <CircleDashed
              className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <span>
              Address pending. The cloud is still creating the load balancer, and this page checks
              again every few seconds.
            </span>
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            Monitoring is not open outside the Kubernetes cluster. Choose Private or Public under
            Settings to open it, or use the port-forward under Advanced.
          </p>
        )}
        <p className="text-sm text-muted-foreground">
          Sign in as <span className="font-mono">{login.username}</span>. Copy password reads the
          password from {environment} and puts it on your clipboard. It is not stored here, and it
          can take up to 20 seconds.
        </p>
        <Button
          type="button"
          variant="outline"
          loading={copying}
          onClick={() => void copyPassword()}
        >
          <Copy strokeWidth={1.5} aria-hidden="true" />
          Copy password
        </Button>
        <details className="space-y-3">
          <summary className="cursor-pointer text-sm font-medium">Advanced</summary>
          <p className="text-sm text-muted-foreground">
            With access to {environment}, read the password and open a local tunnel with these, then
            sign in at <span className="font-mono">{login.tunnelUrl}</span>.
          </p>
          <CommandBlock command={login.passwordCommand} />
          <CommandBlock command={login.portForwardCommand} />
        </details>
      </section>

      <form className="space-y-4" onSubmit={submit} aria-label="Monitoring settings">
        <h3 className="text-base font-semibold">Settings</h3>

        <fieldset className="space-y-3">
          <legend id={`monitoring-access-choice-${environment}`} className="text-sm font-medium">
            Access
          </legend>
          <div className="space-y-1.5">
            <div
              role="radiogroup"
              aria-labelledby={`monitoring-access-choice-${environment}`}
              aria-describedby={`monitoring-access-summary-${environment} monitoring-access-help-${environment}`}
              className="inline-flex gap-1 rounded-md border border-input bg-muted p-0.5"
            >
              {accessChoices.map((choice) => (
                <label
                  key={choice.value}
                  className={cn(
                    "cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium text-muted-foreground transition-colors",
                    "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
                    "has-[:checked]:bg-primary has-[:checked]:text-primary-foreground",
                  )}
                >
                  <input
                    type="radio"
                    name={`monitoring-access-${environment}`}
                    value={choice.value}
                    checked={access === choice.value}
                    onChange={() => {
                      setAccess(choice.value);
                      setRangesError(undefined);
                    }}
                    className="sr-only"
                  />
                  {choice.label}
                </label>
              ))}
            </div>
            <p id={`monitoring-access-summary-${environment}`} className="text-sm">
              {accessSummary[access].description}
            </p>
            {access !== "cluster" ? (
              <p
                id={`monitoring-access-help-${environment}`}
                className="text-sm text-muted-foreground"
              >
                Adds a load balancer in your cloud account, billed by your cloud provider. Private
                and public access are set up for AWS.
              </p>
            ) : null}
          </div>

          {access !== "cluster" ? (
            <div className="space-y-3">
              <Field
                id={`monitoring-ranges-${environment}`}
                label="Allowed addresses"
                help={
                  access === "private"
                    ? "IP addresses or CIDR ranges, separated by commas. Leave empty for the private address ranges."
                    : "IP addresses or CIDR ranges, separated by commas. 0.0.0.0/0 allows any address."
                }
                error={rangesError}
              >
                {(props) => (
                  <Input
                    {...props}
                    autoComplete="off"
                    spellCheck={false}
                    className="font-mono"
                    value={ranges}
                    onChange={(event) => {
                      setRanges(event.target.value);
                      setRangesError(undefined);
                    }}
                  />
                )}
              </Field>
              {openToAnyAddress ? (
                <p className="flex items-start gap-2 text-sm">
                  <TriangleAlert
                    className="mt-0.5 size-4 shrink-0 text-warning"
                    strokeWidth={1.5}
                    aria-hidden="true"
                  />
                  <span>
                    Any address on the internet can connect. Replace 0.0.0.0/0 to limit who can.
                  </span>
                </p>
              ) : null}
            </div>
          ) : null}
        </fieldset>

        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            id={`monitoring-cpu-${environment}`}
            label="CPU (millicores)"
            help="Limit for the monitoring server."
          >
            {(props) => (
              <Input
                {...props}
                type="number"
                className="font-mono"
                value={cpu}
                onChange={(event) => setCpu(event.target.value)}
              />
            )}
          </Field>
          <Field
            id={`monitoring-memory-${environment}`}
            label="Memory (MiB)"
            help="Limit for the monitoring server."
          >
            {(props) => (
              <Input
                {...props}
                type="number"
                className="font-mono"
                value={memory}
                onChange={(event) => setMemory(event.target.value)}
              />
            )}
          </Field>
          <Field
            id={`monitoring-storage-${environment}`}
            label="Storage (GiB)"
            help="Size of the monitoring data volume."
          >
            {(props) => (
              <Input
                {...props}
                type="number"
                className="font-mono"
                value={storage}
                onChange={(event) => setStorage(event.target.value)}
              />
            )}
          </Field>
        </div>
        {save.isError ? (
          <p role="alert" className="text-sm text-destructive">
            {save.error.message}
          </p>
        ) : null}
        <Button type="submit" loading={save.isPending && !confirming}>
          Save settings
        </Button>
      </form>

      <Dialog
        open={confirming !== undefined}
        onOpenChange={(open) => {
          if (!open && !save.isPending) {
            setConfirming(undefined);
          }
        }}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Change access to monitoring</DialogTitle>
            <DialogDescription className="text-foreground">
              {changeConsequence(settings.access, access)}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={save.isPending}
              onClick={() => setConfirming(undefined)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              loading={save.isPending}
              onClick={() => confirming && save.mutate(confirming)}
            >
              Change access
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
