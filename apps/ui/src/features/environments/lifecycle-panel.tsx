import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, CircleCheck, TriangleAlert } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { formatTimestamp } from "@/lib/format";
import { deferUpgrade, putMaintenanceWindow, startRun } from "./api";
import { environmentsKey } from "./environment-context";
import type { Lifecycle, MaintenanceWindow } from "./types";
import { UpdateDialog } from "./update-dialog";

const dayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const deferDays = [7, 14, 30];
const hours = Array.from({ length: 24 }, (_, hour) => hour);

function clock(hour: number): string {
  return `${String(hour).padStart(2, "0")}:00`;
}

/** The time zones the browser knows, for the editor's suggestions. */
function knownZones(): string[] {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return [];
  }
}

/** A moment as the maintenance window's own clock shows it, so the window reads the same everywhere. */
function inZone(iso: string, timeZone: string): string | null {
  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone,
    }).format(new Date(iso));
  } catch {
    return formatTimestamp(iso);
  }
}

type LifecyclePanelProps = {
  environment: string;
  lifecycle: Lifecycle;
};

/**
 * A managed environment's upgrades, its maintenance window and what changed in
 * AWS outside PDS. The changes are reported, never repaired: PDS does not
 * touch what a person did on purpose.
 */
export function LifecyclePanel({ environment, lifecycle }: LifecyclePanelProps) {
  const [updating, setUpdating] = useState<"template" | "kubernetes" | null>(null);
  const driftChecked = formatTimestamp(lifecycle.driftCheckedAt);

  return (
    <div className="space-y-5" data-testid="lifecycle-panel">
      <div className="grid gap-5 md:grid-cols-2">
        <PdsUpgrade environment={environment} lifecycle={lifecycle} />
        <div className="space-y-5">
          <section className="space-y-2" aria-label="Environment template">
            <h3 className="text-sm font-semibold">Environment template</h3>
            {lifecycle.templateOutOfDate ? (
              <>
                <p className="text-sm">
                  {lifecycle.templateSummary ??
                    `Template revision ${lifecycle.templateRevision}, latest ${lifecycle.latestTemplateRevision}`}
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setUpdating("template")}
                >
                  Review update
                </Button>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">
                Template revision {lifecycle.templateRevision} is the latest.
              </p>
            )}
          </section>
          {lifecycle.kubernetesVersion || lifecycle.kubernetesUpgradeAvailable ? (
            <section className="space-y-2" aria-label="Kubernetes">
              <h3 className="text-sm font-semibold">Kubernetes</h3>
              {lifecycle.kubernetesUpgradeAvailable ? (
                <>
                  <p className="text-sm">
                    {lifecycle.kubernetesVersion
                      ? `Kubernetes ${lifecycle.kubernetesVersion} can be upgraded.`
                      : "A Kubernetes upgrade is available."}
                  </p>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setUpdating("kubernetes")}
                  >
                    Upgrade Kubernetes
                  </Button>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Kubernetes {lifecycle.kubernetesVersion} is current.
                </p>
              )}
            </section>
          ) : null}
        </div>
      </div>

      <MaintenanceWindowEditor environment={environment} window={lifecycle.maintenanceWindow} />

      {lifecycle.drift.length > 0 ? (
        <section className="space-y-2" aria-label="Drift">
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            <TriangleAlert className="size-4 text-warning" strokeWidth={1.5} aria-hidden="true" />
            Changed outside PDS
          </h3>
          <ul
            aria-label="Changed outside PDS"
            className="divide-y divide-border rounded-lg border border-border"
          >
            {lifecycle.drift.map((item) => (
              <li key={`${item.resource}-${item.detail}`} className="space-y-0.5 px-4 py-2 text-sm">
                <p className="font-mono">{item.resource}</p>
                <p className="text-muted-foreground">{item.detail}</p>
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted-foreground">
            PDS reports these and does not change them.
            {driftChecked ? ` Checked ${driftChecked}.` : ""}
          </p>
        </section>
      ) : null}

      {updating ? (
        <UpdateDialog
          environment={environment}
          kind={updating}
          currentVersion={lifecycle.kubernetesVersion}
          open
          onOpenChange={(open) => {
            if (!open) {
              setUpdating(null);
            }
          }}
        />
      ) : null}
    </div>
  );
}

function PdsUpgrade({ environment, lifecycle }: LifecyclePanelProps) {
  const queryClient = useQueryClient();
  const daysId = useId();
  const [days, setDays] = useState(30);
  const refresh = () => queryClient.invalidateQueries({ queryKey: environmentsKey });

  const upgrade = useMutation({
    mutationFn: () => startRun(environment, { kind: "upgrade_pds" }),
    onSuccess: async () => {
      toast.success("Upgrading PDS");
      await refresh();
    },
    onError: (error: Error) => toast.error(error.message),
  });
  const defer = useMutation({
    mutationFn: () => deferUpgrade(environment, days),
    onSuccess: async () => {
      toast.success(`Upgrade deferred for ${days} days`);
      await refresh();
    },
    onError: (error: Error) => toast.error(error.message),
  });

  const version = lifecycle.chartVersion;
  const deferredUntil = formatTimestamp(lifecycle.deferredUntil);

  return (
    <section className="space-y-2" aria-label="PDS version">
      <h3 className="text-sm font-semibold">PDS version</h3>
      {lifecycle.pendingChartVersion ? (
        <>
          <p className="text-sm">{`PDS ${lifecycle.pendingChartVersion} is available.`}</p>
          {version ? (
            <p className="text-xs text-muted-foreground">
              Running <span className="font-mono">{version}</span>
            </p>
          ) : null}
          {lifecycle.deferred ? (
            <p className="text-sm text-muted-foreground">
              Deferred until {deferredUntil ?? "later"}. PDS then upgrades it in the maintenance
              window.
            </p>
          ) : (
            <p className="text-sm text-muted-foreground">
              PDS upgrades it in the maintenance window. Upgrade now, or defer it once for up to 30
              days.
            </p>
          )}
          <div className="flex flex-wrap items-end gap-2">
            <Button
              type="button"
              size="sm"
              loading={upgrade.isPending}
              onClick={() => upgrade.mutate()}
            >
              {upgrade.isPending ? "Upgrading..." : "Upgrade now"}
            </Button>
            {lifecycle.deferred ? null : (
              <>
                <div className="space-y-1">
                  <label htmlFor={daysId} className="text-xs text-muted-foreground">
                    Defer for
                  </label>
                  <NativeSelect
                    id={daysId}
                    className="h-8"
                    value={String(days)}
                    onChange={(event) => setDays(Number(event.target.value))}
                  >
                    {deferDays.map((count) => (
                      <option key={count} value={count}>
                        {count} days
                      </option>
                    ))}
                  </NativeSelect>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  loading={defer.isPending}
                  onClick={() => defer.mutate()}
                >
                  Defer
                </Button>
              </>
            )}
          </div>
        </>
      ) : (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <CircleCheck className="size-4 text-success" strokeWidth={1.5} aria-hidden="true" />
          {version ? `PDS ${version} is up to date.` : "PDS is up to date."}
        </p>
      )}
      {lifecycle.skippedChartVersion ? (
        <Note>
          {`PDS ${lifecycle.skippedChartVersion} failed on this environment and was rolled back. PDS does not try it again here.`}
        </Note>
      ) : null}
    </section>
  );
}

function Note({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-start gap-2 text-sm text-muted-foreground">
      <CircleAlert
        className="mt-0.5 size-4 shrink-0 text-warning"
        strokeWidth={1.5}
        aria-hidden="true"
      />
      <span>{children}</span>
    </p>
  );
}

function MaintenanceWindowEditor({
  environment,
  window,
}: {
  environment: string;
  window: MaintenanceWindow;
}) {
  const queryClient = useQueryClient();
  const ids = { day: useId(), hour: useId(), zone: useId() };
  const [editing, setEditing] = useState(false);
  const [day, setDay] = useState(window.day);
  const [hour, setHour] = useState(window.hour);
  const [timeZone, setTimeZone] = useState(window.timeZone);

  const save = useMutation({
    mutationFn: () => putMaintenanceWindow(environment, { day, hour, timeZone: timeZone.trim() }),
    onSuccess: async () => {
      toast.success("Maintenance window saved");
      setEditing(false);
      await queryClient.invalidateQueries({ queryKey: environmentsKey });
    },
  });

  const next = inZone(window.nextStart, window.timeZone);
  const end = inZone(window.nextEnd, window.timeZone);

  return (
    <section className="space-y-2" aria-label="Maintenance window">
      <h3 className="text-sm font-semibold">Maintenance window</h3>
      <p className="text-sm">
        <span className="font-mono">{`${dayNames[window.day] ?? "Sunday"} at ${clock(window.hour)} ${window.timeZone}`}</span>
      </p>
      {next && end ? (
        <p className="text-xs text-muted-foreground">
          Next window <span className="font-mono">{next}</span> to{" "}
          <span className="font-mono">{end}</span>. Upgrades start only inside it.
        </p>
      ) : null}
      {editing ? (
        <form
          className="space-y-3"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            if (timeZone.trim() !== "") {
              save.mutate();
            }
          }}
        >
          <div className="grid gap-3 sm:grid-cols-3">
            <Field id={ids.day} label="Day" help="The day it opens.">
              {(props) => (
                <NativeSelect
                  {...props}
                  className="w-full"
                  value={String(day)}
                  onChange={(event) => setDay(Number(event.target.value))}
                >
                  {dayNames.map((name, value) => (
                    <option key={name} value={value}>
                      {name}
                    </option>
                  ))}
                </NativeSelect>
              )}
            </Field>
            <Field id={ids.hour} label="Hour" help="It stays open two hours.">
              {(props) => (
                <NativeSelect
                  {...props}
                  className="w-full"
                  value={String(hour)}
                  onChange={(event) => setHour(Number(event.target.value))}
                >
                  {hours.map((value) => (
                    <option key={value} value={value}>
                      {clock(value)}
                    </option>
                  ))}
                </NativeSelect>
              )}
            </Field>
            <Field id={ids.zone} label="Time zone" help="An IANA name, like Europe/Berlin.">
              {(props) => (
                <>
                  <Input
                    {...props}
                    className="font-mono"
                    list={`${ids.zone}-zones`}
                    value={timeZone}
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(event) => setTimeZone(event.target.value)}
                  />
                  <datalist id={`${ids.zone}-zones`}>
                    {knownZones().map((zone) => (
                      <option key={zone} value={zone} />
                    ))}
                  </datalist>
                </>
              )}
            </Field>
          </div>
          {save.error ? <p className="text-sm text-destructive">{save.error.message}</p> : null}
          <div className="flex items-center gap-2">
            <Button type="submit" size="sm" loading={save.isPending}>
              {save.isPending ? "Saving..." : "Save window"}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={save.isPending}
              onClick={() => setEditing(false)}
            >
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => {
            setDay(window.day);
            setHour(window.hour);
            setTimeZone(window.timeZone);
            save.reset();
            setEditing(true);
          }}
        >
          Edit window
        </Button>
      )}
    </section>
  );
}
