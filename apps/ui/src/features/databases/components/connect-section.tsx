import { Clock, Copy, Globe, Lock, Network, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useFeature } from "@/features/flags";
import { cn } from "@/lib/utils";
import { allowsAnyAddress, copiesHere, reachabilityOf, visibilityOf } from "../access";
import { fetchDatabaseCredential } from "../api";
import {
  cliCommand,
  connectBlockedReason,
  forwardedCliCommand,
  maskedConnectionUri,
  passwordFromUri,
  passwordMask,
  portForwardCommand,
  readSecretCommand,
  useContextCommand,
} from "../connect";
import type { Database } from "../types";

type ConnectFrom = "laptop" | "app" | "agent";

const origins: readonly { id: ConnectFrom; label: string }[] = [
  { id: "laptop", label: "From your laptop" },
  { id: "app", label: "From your app" },
  { id: "agent", label: "From an AI agent" },
];

/**
 * The first thing on the Overview: how to reach this database, split by where
 * the connection starts. Every command here is a string to paste.
 *
 * No password is ever rendered. A block shows the mask, and the copy click
 * fetches the credential and writes the finished string to the clipboard, so a
 * screen share of this card gives nothing away.
 */
export function ConnectSection({ database }: { database: Database }) {
  const [from, setFrom] = useState<ConnectFrom>("laptop");
  // An agent connects through the MCP server, so with that surface off the
  // way in it would describe is not there.
  const agentsOn = useFeature("mcp");

  return (
    <Card>
      <CardHeader>
        <CardTitle>Connect</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 pt-0">
        <Reachability database={database} />

        {database.connection ? (
          <>
            {/* A toggle rather than tabs: this sits inside the page's own tab
                panel, and a tab strip inside a tab strip reads as two levels
                of the same thing. */}
            <div role="group" aria-label="Connect from" className="flex flex-wrap gap-1">
              {origins
                .filter((origin) => origin.id !== "agent" || agentsOn)
                .map((origin) => (
                  <Button
                    key={origin.id}
                    variant="outline"
                    size="sm"
                    aria-pressed={from === origin.id}
                    className={cn(from === origin.id && "bg-accent")}
                    onClick={() => setFrom(origin.id)}
                  >
                    {origin.label}
                  </Button>
                ))}
            </div>
            <div className="space-y-4">
              {from === "laptop" ? <FromLaptop database={database} /> : null}
              {from === "app" ? <FromApp database={database} /> : null}
              {from === "agent" && agentsOn ? <FromAgent database={database} /> : null}
            </div>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">{connectBlockedReason(database)}</p>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * Writes a value to the clipboard and says so. The value is produced on the
 * click, so a credential is fetched only when someone asks for it and is
 * never held in state.
 */
async function copyToClipboard(
  produce: () => Promise<string>,
  done: string,
  empty: string,
  failed: string,
): Promise<void> {
  try {
    const value = await produce();
    if (!value) {
      toast.error(empty);

      return;
    }

    await navigator.clipboard.writeText(value);
    toast.success(done);
  } catch (error) {
    if (error instanceof Error && error.name === "NotAllowedError") {
      toast.error("The browser blocked the copy. Select the text and copy it.");

      return;
    }

    // The answer says whether this database is still coming up or its
    // password is not held here.
    toast.error(error instanceof Error ? error.message : failed);
  }
}

function FromLaptop({ database }: { database: Database }) {
  const copyCredential = (take: (uri: string) => string, done: string, empty: string) =>
    copyToClipboard(
      async () => take(await fetchDatabaseCredential(database.id)),
      done,
      empty,
      "The connection string could not be copied.",
    );

  return (
    <>
      <p className="text-sm text-muted-foreground">{connectSummary(database)}</p>
      {copiesHere(database) ? (
        <>
          <Block
            label="Connection string"
            value={maskedConnectionUri(database)}
            empty="The connection string cannot be shown here. Copy it to get the real one."
            copyWhenEmpty
            note="The password is hidden. Copy the string to get the real one."
            copyLabel="Copy connection string"
            onCopy={() =>
              copyCredential(
                (uri) => uri,
                "Connection string copied",
                "The connection string is not available yet. Try again in a moment.",
              )
            }
          />
          <Block
            label="Password"
            value={passwordMask}
            empty="The password is not available yet. Try again in a moment."
            note="These are the characters to type. The connection string keeps them encoded."
            copyLabel="Copy password"
            onCopy={() =>
              copyCredential(
                passwordFromUri,
                "Password copied",
                "The password is not available yet. Try again in a moment.",
              )
            }
          />
        </>
      ) : null}
      <KubectlSteps database={database} />
    </>
  );
}

/** What the laptop view says before the commands. The count matches the steps below. */
function connectSummary(database: Database): string {
  const count = reachabilityOf(database) === "external" ? "three" : "four";
  const ending = `They run against that Kubernetes cluster and end at a prompt you can type in.`;
  if (copiesHere(database)) {
    return `This database runs on ${database.environment}. The password stays off this page. Copy it, or use these ${count} commands. ${ending}`;
  }

  return `This database runs on ${database.environment}. No password is held here. These ${count} commands are the way in. ${ending}`;
}

/**
 * The whole path to a shell prompt, in the order you run it: point kubectl at
 * the environment, print the password, forward the port when the database is
 * inside the cluster, then connect.
 *
 * A private database keeps its password on the environment, so these commands are
 * the way in. A public one can also be copied from this page.
 */
function KubectlSteps({ database }: { database: Database }) {
  const environment = database.environment;
  // A database behind a load balancer is reached at its address, which has
  // no Service to forward; only one inside the cluster needs the tunnel.
  const direct = reachabilityOf(database) === "external";
  const forward = direct ? "" : portForwardCommand(database);
  const client = direct ? cliCommand(database, "") : forwardedCliCommand(database);
  const drafted = [
    {
      title: "Point kubectl at the Kubernetes cluster",
      value: useContextCommand(environment),
      note: `Your kubeconfig may call this context something other than ${environment}. Use the name it has there.`,
    },
    { title: "Read the password", value: readSecretCommand(database), note: "" },
    { title: "Forward the port", value: forward, note: "Leave this running in its own shell." },
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
          onCopy={() =>
            copyToClipboard(
              async () => step.value,
              "Command copied",
              "The command could not be copied.",
              "The command could not be copied.",
            )
          }
        />
      ))}
    </div>
  );
}

/**
 * Where an app connects. An app on the same environment uses the address
 * inside the Kubernetes cluster; one elsewhere uses the load balancer's.
 * Either way the password comes from the Secret on the environment, so it
 * stays out of code and images.
 */
function FromApp({ database }: { database: Database }) {
  const connection = database.connection;
  const reachability = reachabilityOf(database);
  if (!connection) {
    return null;
  }
  // The string waits for the address like the host does. A hub has no masked
  // string of its own for a database inside the cluster, so none is shown.
  const shown =
    reachability === "pending"
      ? ""
      : reachability === "external"
        ? maskedConnectionUri(database)
        : (connection.maskedUri ?? "");

  return (
    <>
      <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr]">
        {/* A pending external database publishes its in-cluster host, which a
            client outside the cluster cannot use. */}
        {reachability === "pending" ? (
          <Detail label="Host" value="Pending" pending />
        ) : (
          <Detail label="Host" value={connection.host} />
        )}
        <Detail label="Port" value={String(connection.port)} />
        <Detail label="User" value={connection.username} />
      </dl>
      {shown ? (
        <div className="space-y-1.5">
          <p className="text-sm text-muted-foreground">Connection string</p>
          <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">{shown}</p>
        </div>
      ) : null}
      <p className="text-sm">{appReach(database)}</p>
      <p className="text-sm text-muted-foreground">
        {connection.secretName ? (
          <>
            The password is in the Secret{" "}
            <span className="font-mono text-foreground">{connection.secretName}</span> in namespace{" "}
            <span className="font-mono text-foreground">{database.namespace}</span>. Read it from
            there rather than putting it in your code.
          </>
        ) : (
          <>
            The password is in a Secret in namespace{" "}
            <span className="font-mono text-foreground">{database.namespace}</span>. Step 2 under
            From your laptop names it and reads it.
          </>
        )}
      </p>
    </>
  );
}

function appReach(database: Database): string {
  const reachability = reachabilityOf(database);
  if (reachability === "internal") {
    return `An app on ${database.environment} connects at this address inside the Kubernetes cluster.`;
  }
  if (reachability === "pending") {
    return `The host appears once the load balancer has an address. Until then, only an app on ${database.environment} can connect.`;
  }
  if (visibilityOf(database) === "private") {
    return "An app in your VPC, or a network connected to it, connects at this address.";
  }

  return "An app connects at this address from any network the allow list accepts.";
}

/**
 * An agent connects from the developer's computer with a password the control
 * plane hands over, which only a public database's is. Any other database
 * says so rather than suggesting a prompt that cannot work.
 */
function FromAgent({ database }: { database: Database }) {
  if (!copiesHere(database)) {
    return (
      <p className="text-sm text-muted-foreground">
        An AI agent connects from your computer with a password the control plane hands over. The
        password for <span className="font-mono text-foreground">{database.name}</span> stays on the
        Kubernetes cluster it runs on, so connect with the steps under From your laptop.
      </p>
    );
  }

  return (
    <>
      <div className="space-y-1.5">
        <p className="text-sm">With the pds plugin in Claude Code or Cursor, ask:</p>
        <p className="rounded-md bg-muted px-3 py-2 font-mono text-xs">
          Connect to {database.name} on {database.environment}.
        </p>
      </div>
      <p className="text-sm text-muted-foreground">
        The plugin fetches the password, keeps it in your computer&apos;s credential store and runs
        queries from your computer. It signs in as you.
      </p>
      <Button variant="outline" asChild>
        <Link to="/agents">Set up an agent</Link>
      </Button>
    </>
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

  const copy = (
    <Button variant="outline" loading={copying} aria-label={copyLabel} onClick={() => void run()}>
      {copying ? null : <Copy strokeWidth={1.5} aria-hidden="true" />}
      Copy
    </Button>
  );

  // An empty mono box says nothing. The sentence says which of the two
  // reasons it is, and a copy that can only fail is not offered.
  if (!value) {
    return (
      <div className="space-y-1.5">
        <p className="text-sm text-muted-foreground">{label}</p>
        <p className="text-sm">{empty}</p>
        {note ? <p className="text-xs text-muted-foreground">{note}</p> : null}
        {copyWhenEmpty ? copy : null}
      </div>
    );
  }

  return (
    <div className="space-y-1.5">
      <p className="text-sm text-muted-foreground">{label}</p>
      <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">{value}</p>
      {note ? <p className="text-xs text-muted-foreground">{note}</p> : null}
      {copy}
    </div>
  );
}

type DetailProps = {
  label: string;
  value: string;
  /** A placeholder rather than a value, so it is not set in mono. */
  pending?: boolean;
};

function Detail({ label, value, pending }: DetailProps) {
  return (
    <div className="space-y-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={pending ? "text-sm text-muted-foreground" : "break-all font-mono text-sm"}>
        {value}
      </dd>
    </div>
  );
}

/**
 * Where the database can be reached from, as icon and text. It renders before
 * the connection exists too, because whether a database is private or public
 * is decided at create and is worth knowing while it comes up. The allow list
 * belongs to a public database only.
 */
function Reachability({ database }: { database: Database }) {
  const reachability = reachabilityOf(database);
  const visibility = visibilityOf(database);
  const ranges = database.access?.sourceRanges ?? [];
  const allowList = visibility === "public" ? <AllowedFrom ranges={ranges} /> : null;

  if (reachability === "internal") {
    return (
      <p className="flex items-center gap-2 text-sm">
        <Lock
          className="size-4 shrink-0 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
          data-testid="reachability-icon-internal"
        />
        Reachable inside the Kubernetes cluster only
      </p>
    );
  }

  if (reachability === "pending") {
    return (
      <div className="space-y-1">
        <p className="flex items-center gap-2 text-sm">
          <Clock
            className="size-4 shrink-0 text-muted-foreground"
            strokeWidth={1.5}
            aria-hidden="true"
            data-testid="reachability-icon-pending"
          />
          {visibility === "private" ? "Private address pending" : "Public address pending"}
        </p>
        <p className="text-sm text-muted-foreground">
          The host appears here once the load balancer has an address. Until then, connect from
          inside the Kubernetes cluster.
        </p>
        {allowList}
      </div>
    );
  }

  if (visibility === "private") {
    return (
      <p className="flex items-center gap-2 text-sm">
        <Network
          className="size-4 shrink-0 text-success"
          strokeWidth={1.5}
          aria-hidden="true"
          data-testid="reachability-icon-private"
        />
        Private, reachable from your VPC
      </p>
    );
  }

  return (
    <div className="space-y-1">
      <p className="flex items-center gap-2 text-sm">
        <Globe
          className="size-4 shrink-0 text-success"
          strokeWidth={1.5}
          aria-hidden="true"
          data-testid="reachability-icon-public"
        />
        Public, reachable from the internet
      </p>
      {allowList}
    </div>
  );
}

function AllowedFrom({ ranges }: { ranges: string[] }) {
  // No list at all is what an adapter older than allow lists sends, and the
  // load balancer it made then took any address.
  const anyAddress = ranges.length === 0 || allowsAnyAddress(ranges);
  const warning = anyAddress ? (
    <p className="flex items-center gap-2 text-sm text-muted-foreground">
      <TriangleAlert
        className="size-4 shrink-0 text-warning"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      Any address on the internet can connect.
    </p>
  ) : null;

  if (ranges.length === 0) {
    return warning;
  }

  return (
    <>
      <p className="text-sm text-muted-foreground">
        Allowed from{" "}
        <span className="break-all font-mono text-foreground">{ranges.join(", ")}</span>
      </p>
      {warning}
    </>
  );
}
