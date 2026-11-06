import { Copy } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { copiesHere, reachabilityOf, visibilityOf } from "../access";
import { fetchDatabaseCredential } from "../api";
import {
  cliCommand,
  forwardedCliCommand,
  maskedConnectionUri,
  passwordFromUri,
  passwordMask,
  portForwardCommand,
  readSecretCommand,
  useContextCommand,
} from "../connect";
import type { Database } from "../types";

type ConnectDialogProps = {
  database: Database;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called after something reaches the clipboard, never after a failure. */
  onCopied?: () => void;
};

/**
 * Everything a developer needs to reach this database, as strings they can
 * paste. The string itself is one of three answers: a client command and a
 * port-forward are the two that actually work from a laptop.
 *
 * No password is ever rendered. Each block shows the mask, and the copy click
 * fetches the credential and writes the finished string to the clipboard, so a
 * screen share of this dialog gives nothing away.
 */
export function ConnectDialog({ database, open, onOpenChange, onCopied }: ConnectDialogProps) {
  const { connection } = database;
  const environment = database.environment;

  async function copy(
    produce: () => Promise<string>,
    done: () => void,
    empty: string,
  ): Promise<void> {
    try {
      const value = await produce();
      if (!value) {
        toast.error(empty);

        return;
      }

      await navigator.clipboard.writeText(value);
      done();
      onCopied?.();
    } catch (error) {
      if (error instanceof Error && error.name === "NotAllowedError") {
        toast.error("The browser blocked the copy. Select the text and copy it.");

        return;
      }

      // The answer says whether this database is still coming up or its
      // password is not held here.
      toast.error(
        error instanceof Error ? error.message : "The connection string could not be copied.",
      );
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>Connect to {database.name}</DialogTitle>
          <DialogDescription>{connectSummary(database)}</DialogDescription>
        </DialogHeader>

        {connection ? (
          <div className="space-y-4">
            {copiesHere(database) ? (
              <Block
                label="Connection string"
                value={maskedConnectionUri(database)}
                empty="The connection string cannot be shown here. Copy it to get the real one."
                copyWhenEmpty
                note="The password is hidden. Copy the string to get the real one."
                copyLabel="Copy connection string"
                onCopy={() =>
                  copy(
                    () => fetchDatabaseCredential(database.id),
                    () => toast.success("Connection string copied"),
                    "The connection string is not available yet. Try again in a moment.",
                  )
                }
              />
            ) : null}
            {copiesHere(database) ? (
              <Block
                label="Password"
                value={passwordMask}
                empty="The password is not available yet. Try again in a moment."
                note="These are the characters to type. The connection string keeps them encoded."
                copyLabel="Copy password"
                onCopy={() =>
                  copy(
                    async () => passwordFromUri(await fetchDatabaseCredential(database.id)),
                    () => toast.success("Password copied"),
                    "The password is not available yet. Try again in a moment.",
                  )
                }
              />
            ) : null}
            <SpokeSteps database={database} environment={environment} onCopied={onCopied} />
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">
            Connection details are not published yet. They appear once the database is ready.
          </p>
        )}
      </DialogContent>
    </Dialog>
  );
}

type BlockProps = {
  label: string;
  value: string;
  /** Said in place of the string when there is none to show. */
  empty: string;
  /**
   * Whether a copy still works with nothing on screen. True where the string
   * is fetched rather than shown, so a mask this could not build says nothing
   * about whether the real one can be had.
   */
  copyWhenEmpty?: boolean;
  note?: string;
  copyLabel: string;
  onCopy: () => Promise<void>;
};

/** What the dialog says before the commands. The count matches the steps below. */
function connectSummary(database: Database): string {
  const count = reachabilityOf(database) === "external" ? "three" : "four";
  const ending = `They run against that Kubernetes cluster and end at a prompt you can type in.`;
  if (copiesHere(database)) {
    return `This database runs on ${database.environment}. The password stays off this page. Copy it, or use these ${count} commands. ${ending}`;
  }

  return `This database runs on ${database.environment}. No password is held here. These ${count} commands are the way in. ${ending}`;
}

/** One thing to paste: what it is, the string itself, and a copy for it. */
function Block({ label, value, empty, copyWhenEmpty, note, copyLabel, onCopy }: BlockProps) {
  const [copying, setCopying] = useState(false);

  async function run(): Promise<void> {
    setCopying(true);
    try {
      await onCopy();
    } finally {
      setCopying(false);
    }
  }

  // An empty mono box says nothing. The sentence says which of the two
  // reasons it is, and a copy that can only fail is not offered.
  if (!value) {
    return (
      <div className="space-y-1.5">
        <p className="text-sm text-muted-foreground">{label}</p>
        <p className="text-sm">{empty}</p>
        {note ? <p className="text-xs text-muted-foreground">{note}</p> : null}
        {copyWhenEmpty ? (
          <Button
            variant="outline"
            loading={copying}
            aria-label={copyLabel}
            onClick={() => void run()}
          >
            {copying ? null : <Copy strokeWidth={1.5} aria-hidden="true" />}
            Copy
          </Button>
        ) : null}
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">{value}</p>
      {note ? <p className="text-xs text-muted-foreground">{note}</p> : null}
      <Button variant="outline" loading={copying} aria-label={copyLabel} onClick={() => void run()}>
        {copying ? null : <Copy strokeWidth={1.5} aria-hidden="true" />}
        Copy
      </Button>
    </div>
  );
}

type SpokeStepsProps = {
  database: Database;
  environment: string;
  onCopied?: () => void;
};

/**
 * The whole path to a shell prompt, in the order you run it: point kubectl at
 * the environment, print the password, forward the port when the database is
 * inside the cluster, then connect.
 *
 * A private database keeps its password on the environment, so these commands are
 * the way in. A public one can also be copied from this page.
 */
function SpokeSteps({ database, environment, onCopied }: SpokeStepsProps) {
  const context = useContextCommand(environment);
  const secret = readSecretCommand(database);
  // A database behind a load balancer is reached at its address, which has
  // no Service to forward; only one inside the cluster needs the tunnel.
  const direct = reachabilityOf(database) === "external";
  const forward = direct ? "" : portForwardCommand(database);
  const client = direct ? cliCommand(database, "") : forwardedCliCommand(database);
  const drafted = [
    {
      title: "Point kubectl at the Kubernetes cluster",
      value: context,
      note: `Your kubeconfig may call this context something other than ${environment}. Use the name it has there.`,
    },
    {
      title: "Read the password",
      value: secret,
      note: "",
    },
    {
      title: "Forward the port",
      value: forward,
      note: "Leave this running in its own shell.",
    },
    {
      title: "Connect",
      value: client,
      note:
        direct && visibilityOf(database) === "private"
          ? "Run this from your VPC or a network connected to it. It asks for the password from step 2."
          : "This asks for the password from step 2.",
    },
  ].filter((step) => step.value !== "");
  const connectAt = drafted.length;
  const steps = drafted.map((step, index) => ({
    label: `${index + 1}. ${step.title}`,
    value: step.value,
    note:
      step.title === "Read the password"
        ? `This prints the password on its own, ready to paste at the prompt in step ${connectAt}. The instance names the Secret, so nothing is guessed.`
        : step.note,
  }));

  async function copy(value: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      toast.success("Command copied");
      onCopied?.();
    } catch (error) {
      if (error instanceof Error && error.name === "NotAllowedError") {
        toast.error("The browser blocked the copy. Select the text and copy it.");

        return;
      }

      toast.error("The command could not be copied.");
    }
  }

  return (
    <div className="space-y-4">
      {steps.map((step) => (
        <Block
          key={step.label}
          label={step.label}
          value={step.value}
          empty=""
          note={step.note}
          copyLabel={`Copy step ${step.label.slice(0, 1)}`}
          onCopy={() => copy(step.value)}
        />
      ))}
      {direct ? null : (
        <p className="text-xs text-muted-foreground">
          Address inside the Kubernetes cluster:{" "}
          <span className="font-mono">
            {database.connection?.host}:{database.connection?.port}
          </span>{" "}
          as <span className="font-mono">{database.connection?.username}</span>. Use it from a
          workload on {environment} instead of steps 3 and 4.
        </p>
      )}
    </div>
  );
}
