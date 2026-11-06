import { Clock, Copy, Globe, Lock, Network, Plug, TriangleAlert } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { copiesHere, reachabilityOf, visibilityOf } from "../access";
import { fetchDatabaseCredential } from "../api";
import { connectBlockedReason, maskedConnectionUri, passwordFromUri } from "../connect";
import type { Database } from "../types";

type ConnectCardProps = {
  database: Database;
  /** Opens the connect dialog, which is where every copy happens. */
  onConnect: () => void;
  className?: string;
};

/**
 * Where a create lands. It states the shape of the connection, with the
 * password masked.
 *
 * A database whose load balancer has an address shows that string here. Where
 * this page can read the password, copying it fetches the password; a hub's
 * private database copies from the dialog instead. Every other database still
 * copies from the dialog, which is the one place an in-cluster credential
 * leaves the page.
 */
export function ConnectCard({ database, onConnect, className }: ConnectCardProps) {
  const { connection } = database;
  const reachability = reachabilityOf(database);

  return (
    <Card className={className}>
      <CardHeader>
        <CardTitle>Connect</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 pt-0">
        <Reachability database={database} />

        {connection ? (
          <>
            <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-[2fr_1fr_1fr]">
              {/* A pending external database publishes its in-cluster host,
                  which a client outside the cluster cannot use. */}
              {reachability === "pending" ? (
                <Detail label="Host" value="Pending" pending />
              ) : (
                <Detail label="Host" value={connection.host} />
              )}
              <Detail label="Port" value={String(connection.port)} />
              <Detail label="User" value={connection.username} />
            </dl>

            {/* The string carries the same in-cluster host, so it waits for
                the address too. The dialog still has it for a workload inside
                the environment. */}
            {copiesHere(database) ? (
              <ReachableConnection database={database} />
            ) : reachability === "external" ? (
              <div className="space-y-1.5">
                <p className="text-sm text-muted-foreground">Connection string</p>
                <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">
                  {maskedConnectionUri(database)}
                </p>
                <p className="text-sm text-muted-foreground">
                  The password stays on the Kubernetes cluster this database runs on. Connect shows
                  how to read it there.
                </p>
              </div>
            ) : connection.maskedUri && reachability !== "pending" ? (
              <div className="space-y-1.5">
                <p className="text-sm text-muted-foreground">Connection string</p>
                <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">
                  {connection.maskedUri}
                </p>
              </div>
            ) : null}

            {/* The header carries the primary Connect on this page, and one
                view gets one purple button. */}
            <Button variant="outline" onClick={onConnect}>
              <Plug strokeWidth={1.5} aria-hidden="true" />
              Connect
            </Button>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">{connectBlockedReason(database)}</p>
        )}
      </CardContent>
    </Card>
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

/**
 * The URL a client outside the cluster uses. The password on screen is the
 * mask. Copy replaces it with the real one, which is never rendered.
 */
function ReachableConnection({ database }: { database: Database }) {
  const [copying, setCopying] = useState(false);
  const shown = maskedConnectionUri(database);

  async function copy(take: (uri: string) => string, done: string, empty: string): Promise<void> {
    setCopying(true);
    try {
      const uri = await fetchDatabaseCredential(database.id);
      const value = uri ? take(uri) : "";
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

      toast.error(
        error instanceof Error ? error.message : "The connection string could not be copied.",
      );
    } finally {
      setCopying(false);
    }
  }

  return (
    <div className="space-y-2">
      <div className="space-y-1.5">
        <p className="text-sm text-muted-foreground">Connection string</p>
        <p className="break-all rounded-md bg-muted px-3 py-2 font-mono text-xs">{shown}</p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          onClick={() =>
            void copy(
              (uri) => uri,
              "Connection string copied",
              "The connection string is not available yet. Try again in a moment.",
            )
          }
          disabled={copying || !shown}
        >
          <Copy strokeWidth={1.5} aria-hidden="true" />
          Copy connection string
        </Button>
        <Button
          variant="outline"
          onClick={() =>
            void copy(
              passwordFromUri,
              "Password copied",
              "The password is not available yet. Try again in a moment.",
            )
          }
          disabled={copying || !shown}
        >
          <Copy strokeWidth={1.5} aria-hidden="true" />
          Copy password
        </Button>
      </div>
    </div>
  );
}

function AllowedFrom({ ranges }: { ranges: string[] }) {
  if (ranges.length === 0) {
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <TriangleAlert
          className="size-4 shrink-0 text-warning"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        Any address on the internet can connect.
      </p>
    );
  }

  return (
    <p className="text-sm text-muted-foreground">
      Allowed from <span className="break-all font-mono text-foreground">{ranges.join(", ")}</span>
    </p>
  );
}
