import { CircleAlert } from "lucide-react";
import { useEffect, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { UnreachableBanner, useEnvironments } from "@/features/environments";
import {
  DatabaseHeader,
  DatabaseTabs,
  DeleteDialog,
  ProvisioningScreen,
  RestoreDialog,
  ScaleDialog,
  isProvisioningComplete,
  useCreateBackup,
  useDatabase,
  useProvisioningEvents,
} from "@/features/databases";
import type {
  Backup,
  Database,
  DatabaseTab,
  NavigableIntent,
  ScaleField,
} from "@/features/databases";
import type { MetricKind } from "@/features/monitoring";
import {
  buildTicketContext,
  ContactSupportDialog,
  useSupportAvailability,
} from "@/features/support";
import { formatTimestamp } from "@/lib/format";

/**
 * The database detail page. It has two bodies: the provisioning screen while
 * a create is running or after it failed, and the tabbed detail once the
 * database has published its connection.
 */
export function DatabaseDetailPage() {
  const params = useParams();
  const id = params.environment && params.id ? `${params.environment}/${params.id}` : "";
  const listHref = params.environment
    ? `/environments/${encodeURIComponent(params.environment)}`
    : "/environments";
  const location = useLocation();
  const navigate = useNavigate();
  const chatIntent = (location.state as { chatIntent?: NavigableIntent } | null)?.chatIntent;

  const [tab, setTab] = useState<DatabaseTab>("overview");
  const [scaleOpen, setScaleOpen] = useState(false);
  const [scaleField, setScaleField] = useState<ScaleField>("cpu");
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [supportOpen, setSupportOpen] = useState(false);
  const [supportPrefill, setSupportPrefill] = useState<
    { subject: string; description: string } | undefined
  >(undefined);
  // Where a chat action sends the developer: one chart on the Metrics tab.
  const [metricsFocus, setMetricsFocus] = useState<MetricKind | undefined>(undefined);
  // The dialog is opened from a row, so the row it was opened on is what says
  // which backup a restore reads from.
  const [restoreBackup, setRestoreBackup] = useState<Backup | undefined>(undefined);
  const { environments } = useEnvironments();
  const environment = params.environment
    ? environments.find((item) => item.name === params.environment)
    : undefined;
  const unreachable = environment?.status === "unreachable";

  const { data, error, isPending, isFetching, refetch } = useDatabase(id);
  const { events, lastActiveStep, failureSettled } = useProvisioningEvents(data);
  const backup = useCreateBackup(id);
  // The header leaves Contact support out when no ticket can be sent.
  const support = useSupportAvailability();

  // A backup takes minutes, so the answer to the button is the pending row on
  // the Backups tab rather than a spinner the developer has to sit through.
  const startBackup = () => {
    backup.mutate(undefined, {
      onSuccess: () => {
        setTab("backups");
        toast.success("Backup started");
      },
      onError: (failed) => toast.error(failed.message),
    });
  };

  // Every chat action that leaves its own block. Each one lands on the
  // control that performs the fix rather than on the screen it lives on, so
  // "Scale storage" opens the dialog on storage.
  const runAction = (intent: NavigableIntent) => {
    switch (intent.kind) {
      case "scale":
        setScaleField(intent.field);
        setScaleOpen(true);
        break;
      case "backup":
        startBackup();
        break;
      case "backups":
        setTab("backups");
        break;
      case "metrics":
        setMetricsFocus(intent.metric);
        setTab("metrics");
        break;
      case "status":
        setTab("overview");
        break;
      case "support":
        setSupportPrefill(
          intent.subject || intent.description
            ? { subject: intent.subject ?? "", description: intent.description ?? "" }
            : undefined,
        );
        setSupportOpen(true);
        break;
    }
  };

  useEffect(() => {
    if (!chatIntent) {
      return;
    }

    runAction(chatIntent);
    navigate(".", { replace: true, state: null });
  }, [chatIntent, navigate]);

  if (isPending) {
    return <DetailSkeleton />;
  }

  if (error) {
    return (
      <Card className="mx-auto flex w-full max-w-md flex-col items-center gap-3 px-6 py-12 text-center">
        <CircleAlert
          className="size-5 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <div className="space-y-1">
          <p className="text-sm font-medium">This database could not be read</p>
          <p className="text-sm text-muted-foreground">{error.message}</p>
        </div>
        <Button variant="outline" loading={isFetching} onClick={() => void refetch()}>
          Try again
        </Button>
      </Card>
    );
  }

  // A database that has never published a connection is still coming up, or
  // failed on the way. Either way the provisioning screen is the right body.
  if (!isProvisioningComplete(data) && !data.connection) {
    return (
      <>
        <ProvisioningScreen
          database={data}
          events={events}
          failedStep={lastActiveStep}
          failed={failureSettled}
          onRetry={() => void refetch()}
          retrying={isFetching}
          onDelete={() => setDeleteOpen(true)}
        />
        <DeleteDialog
          database={data}
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          onDeleted={() => void navigate(listHref, { replace: true })}
        />
      </>
    );
  }

  return (
    <div className="animate-fade-in space-y-6">
      {unreachable ? (
        <UnreachableBanner lastSeen={formatTimestamp(environment?.lastSeen) ?? undefined} />
      ) : null}
      <DatabaseHeader
        database={data}
        onScale={() => setScaleOpen(true)}
        onBackUpNow={startBackup}
        backingUp={backup.isPending}
        backupEnabled={!unreachable}
        onDelete={() => setDeleteOpen(true)}
        onContactSupport={
          support.connected
            ? () => {
                setSupportPrefill(undefined);
                setSupportOpen(true);
              }
            : undefined
        }
      />
      <ScaleDialog
        database={data}
        open={scaleOpen}
        onOpenChange={setScaleOpen}
        focusField={scaleField}
      />
      <DeleteDialog
        database={data}
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        // The database this page reads is gone, so staying here would only show
        // its own 404. Replace rather than push: Back must not return to it.
        onDeleted={() => void navigate(listHref, { replace: true })}
      />
      <RestoreDialog
        database={data}
        backup={restoreBackup}
        open={restoreBackup != null}
        onOpenChange={(open) => {
          if (!open) {
            setRestoreBackup(undefined);
          }
        }}
      />
      <ContactSupportDialog
        open={supportOpen}
        onOpenChange={setSupportOpen}
        subject={supportPrefill?.subject || supportSubject(data)}
        description={supportPrefill?.description || supportDescription(data)}
        context={buildTicketContext(data, { errorMessage: data.message })}
        connected={support.connected}
        portalUrl={support.portalUrl}
      />
      <DatabaseTabs
        database={data}
        tab={tab}
        onTabChange={setTab}
        onBackUpNow={startBackup}
        backingUp={backup.isPending}
        backupEnabled={!unreachable}
        onRestore={unreachable ? undefined : setRestoreBackup}
        onDelete={() => setDeleteOpen(true)}
        metricsFocus={metricsFocus}
      />
    </div>
  );
}

/**
 * The ticket a Contact support action opens with. Support asks for the same
 * facts every time, so the dialog states them and the developer edits rather
 * than retypes.
 *
 * The connection details are left out on purpose: the URI carries the password,
 * and a ticket is the last place a credential should end up.
 */
function supportSubject(database: Database): string {
  return `A node of ${database.name} is down`;
}

function supportDescription(database: Database): string {
  return [
    `Database: ${database.name}`,
    `Id: ${database.id}`,
    `Engine: ${database.engine}${database.version ? ` ${database.version}` : ""}`,
    `Profile: ${database.profile}`,
    `Namespace: ${database.namespace}`,
    `Members: ${database.members}`,
    `Size: ${database.cpu} CPU, ${database.memoryGb} GB memory, ${database.storageGb} GB storage`,
    `Status: ${database.status}`,
    database.message ? `Last message: ${database.message}` : "",
  ]
    .filter((line) => line !== "")
    .join("\n");
}

function DetailSkeleton() {
  return (
    <div className="space-y-6" data-testid="database-detail-loading">
      <div className="flex items-start justify-between gap-4">
        <div className="space-y-2">
          <Skeleton className="h-8 w-56" />
          <Skeleton className="h-5 w-64" />
        </div>
        <Skeleton className="h-9 w-64 rounded-md" />
      </div>
      <Skeleton className="h-9 w-full" />
      <Card>
        <div className="space-y-4 px-5 py-5">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-9 w-32 rounded-md" />
        </div>
      </Card>
    </div>
  );
}
