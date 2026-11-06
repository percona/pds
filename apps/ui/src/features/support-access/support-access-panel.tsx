import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleX, Loader, ShieldCheck, ShieldOff } from "lucide-react";
import { useEffect, useState, type FormEvent } from "react";
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
import { NativeSelect } from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiError } from "@/lib/api";
import { CommandBlock, environmentsKey } from "@/features/environments";
import {
  endSupportAccess,
  extendSupportAccess,
  fetchSupportAccess,
  openSupportAccess,
} from "./api";
import { formatTimeLeft, useNow } from "./countdown";
import {
  extendLabel,
  hoursLabel,
  levelFacts,
  levelName,
  maxSupportHours,
  recordNote,
  startFailed,
  supportHours,
} from "./copy";
import type { SupportLevel, SupportWindow } from "./types";

/** How often the panel looks again while a window is still starting. */
const supportAccessPollMs = 3_000;

const levels: SupportLevel[] = ["readOnly", "admin"];

function supportAccessKey(environment: string) {
  return ["environments", environment, "support-access"] as const;
}

/** Why a closed window closed, in a sentence that says what is true now. */
function closedMessage(reason: string | undefined): string {
  switch (reason) {
    case "expired":
      return "Access ended when the time ran out. Percona can no longer reach this environment.";
    case "removed":
      return "The environment was removed, so access ended.";
    case "failed":
      return startFailed;
    default:
      return "Access was ended. Percona can no longer reach this environment.";
  }
}

function endsAt(expiresAt: string): string {
  const date = new Date(expiresAt);
  const time = date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });

  return date.toDateString() === new Date().toDateString()
    ? `today at ${time}`
    : `${date.toLocaleDateString([], { weekday: "short" })} at ${time}`;
}

/**
 * An environment's support access: open it, see that it is on, end it. Anyone
 * who can see the environment sees the state. `canChange` is the admin check
 * the console makes to decide what to offer; the control plane enforces it.
 */
export function SupportAccessPanel({
  environment,
  canChange,
}: {
  environment: string;
  canChange: boolean;
}) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: supportAccessKey(environment),
    queryFn: ({ signal }) => fetchSupportAccess(environment, signal),
    // The environment confirms a start or an extension on its next beat, so
    // only this read polls while a window opens or extends. An open one is
    // read again once it has expired, which is when the control plane reports
    // it closed. The environments list keeps its own pace.
    refetchInterval: (current) => {
      const data = current.state.data;
      if (data?.state === "opening" || (data?.state === "open" && data.extendingTo)) {
        return supportAccessPollMs;
      }
      if (data?.state === "open") {
        return Math.max(1_000, new Date(data.expiresAt).getTime() - Date.now() + 1_000);
      }

      return false;
    },
  });
  const state = query.data?.state;

  // The list's one-line status follows the window, so it refreshes when the
  // window changes state rather than on a timer of its own.
  useEffect(() => {
    if (state) {
      void queryClient.invalidateQueries({ queryKey: environmentsKey });
    }
  }, [state, queryClient]);

  if (query.isPending) {
    return (
      <div data-testid="support-access-loading" className="space-y-3">
        <Skeleton className="h-4 w-48" />
        <Skeleton className="h-9 w-full" />
      </div>
    );
  }
  // An environment of another Team answers 404, and that Team is told nothing.
  if (query.error instanceof ApiError && query.error.status === 404) {
    return null;
  }
  if (query.isError) {
    return (
      <p role="alert" className="text-sm text-destructive">
        Support access could not be read. {query.error.message}
      </p>
    );
  }

  return (
    <SupportAccessState
      environment={environment}
      access={query.data}
      canChange={canChange}
      onChanged={(next) => queryClient.setQueryData(supportAccessKey(environment), next)}
    />
  );
}

function StateIcon({ state }: { state: SupportWindow["state"] | undefined }) {
  const common = { strokeWidth: 1.5, "aria-hidden": true } as const;
  if (state === "open") {
    return <ShieldCheck {...common} className="size-4 shrink-0 text-warning" />;
  }
  if (state === "opening") {
    return <Loader {...common} className="size-4 shrink-0 animate-pulse" />;
  }

  return <ShieldOff {...common} className="size-4 shrink-0 text-muted-foreground" />;
}

function SupportAccessState({
  environment,
  access: current,
  canChange,
  onChanged,
}: {
  environment: string;
  access: SupportWindow | null;
  canChange: boolean;
  onChanged: (next: SupportWindow | null) => void;
}) {
  const queryClient = useQueryClient();
  const [ending, setEnding] = useState(false);
  const end = useMutation({
    mutationFn: () => endSupportAccess(environment),
    onSuccess: async () => {
      setEnding(false);
      await queryClient.invalidateQueries({ queryKey: supportAccessKey(environment) });
    },
  });
  const state = current?.state;
  const active = state === "opening" || state === "open";

  return (
    <div className="space-y-5">
      <h3 className="flex items-center gap-2 text-base font-semibold">
        <StateIcon state={state} />
        Support access
      </h3>

      {state === "opening" ? (
        <p className="text-sm text-muted-foreground">
          Support access is starting. Access begins when the environment confirms, which can take a
          few minutes.
        </p>
      ) : null}
      {current && state === "open" ? (
        <OpenWindow
          environment={environment}
          access={current}
          canChange={canChange}
          onChanged={onChanged}
        />
      ) : null}
      {!active ? (
        <p className="flex items-start gap-2 text-sm text-muted-foreground">
          {state === "closed" && current?.closedReason === "failed" ? (
            <CircleX
              className="mt-0.5 size-4 shrink-0 text-destructive"
              strokeWidth={1.5}
              aria-hidden="true"
            />
          ) : null}
          <span>
            {state === "closed"
              ? closedMessage(current?.closedReason)
              : "Support access is off. Percona cannot reach this environment."}
          </span>
        </p>
      ) : null}

      {canChange && !active ? <OpenForm environment={environment} onOpened={onChanged} /> : null}
      {active ? <p className="text-xs text-muted-foreground">{recordNote}</p> : null}
      {canChange && active ? (
        <Button type="button" variant="outline" onClick={() => setEnding(true)}>
          End access
        </Button>
      ) : null}

      <Dialog
        open={ending}
        onOpenChange={(open) => {
          if (!open && !end.isPending) {
            setEnding(false);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>End support access?</DialogTitle>
            <DialogDescription className="text-foreground">
              The support engineer loses access to <span className="font-mono">{environment}</span>{" "}
              at once. To give access again, open a new one.
            </DialogDescription>
          </DialogHeader>
          {end.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {end.error.message}
            </p>
          ) : null}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={end.isPending}
              onClick={() => setEnding(false)}
            >
              Cancel
            </Button>
            {/* The one destructive button that is not a delete: ending access
                cannot be undone, and the engineer is cut off at once. */}
            <Button
              type="button"
              variant="destructive"
              loading={end.isPending}
              onClick={() => end.mutate()}
            >
              End access
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function OpenWindow({
  environment,
  access: current,
  canChange,
  onChanged,
}: {
  environment: string;
  access: SupportWindow;
  canChange: boolean;
  onChanged: (next: SupportWindow) => void;
}) {
  const now = useNow();

  return (
    <div className="space-y-3">
      <p className="text-sm">
        Support access is open for{" "}
        <span className="font-mono">{formatTimeLeft(current.expiresAt, now)}</span>, until{" "}
        {endsAt(current.expiresAt)}.
      </p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
        <dt className="text-muted-foreground">Level</dt>
        <dd>{levelName[current.level]}</dd>
        {current.caseReference ? (
          <>
            <dt className="text-muted-foreground">Case</dt>
            <dd className="font-mono">{current.caseReference}</dd>
          </>
        ) : null}
      </dl>
      <p className="text-sm text-muted-foreground">
        {levelFacts[current.level]} To change the level, end access and open it again.
      </p>
      {current.shareNote ? (
        <div className="space-y-2">
          <h4 className="text-sm font-medium">Share with Percona</h4>
          <p className="text-sm text-muted-foreground">
            The support engineer needs this to connect. Send it to them if they do not already have
            it.
          </p>
          <CommandBlock command={current.shareNote} label="Copy details" copied="Details copied" />
        </div>
      ) : null}
      {current.extendingTo ? (
        <p className="text-sm text-muted-foreground">
          Extending until {endsAt(current.extendingTo)}. The countdown moves once the environment
          confirms.
        </p>
      ) : null}
      {canChange ? (
        <ExtendForm environment={environment} access={current} now={now} onExtended={onChanged} />
      ) : null}
    </div>
  );
}

/** Whether ending `hours` from now would be later than the window ends now. */
function canExtendTo(hours: number, expiresAt: string, now: number): boolean {
  return now + hours * 3_600_000 > new Date(expiresAt).getTime();
}

function ExtendForm({
  environment,
  access: current,
  now,
  onExtended,
}: {
  environment: string;
  access: SupportWindow;
  now: number;
  onExtended: (next: SupportWindow) => void;
}) {
  const [chosen, setChosen] = useState<(typeof supportHours)[number]>(4);
  const offered = supportHours.filter((hours) => canExtendTo(hours, current.expiresAt, now));
  const hours = offered.includes(chosen) ? chosen : offered[0];
  const extend = useMutation({
    mutationFn: (until: number) => extendSupportAccess(environment, until),
    onSuccess: onExtended,
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (hours !== undefined) {
      extend.mutate(hours);
    }
  }

  return (
    <form
      className="flex flex-wrap items-end gap-3"
      onSubmit={submit}
      aria-label="Extend support access"
    >
      <Field
        id={`support-extend-${environment}`}
        label="Extend access"
        help={`The most is ${maxSupportHours} hours from now.`}
      >
        {(props) => (
          <NativeSelect
            {...props}
            value={hours ?? ""}
            onChange={(event) =>
              setChosen(Number(event.target.value) as (typeof supportHours)[number])
            }
          >
            {supportHours.map((choice) => (
              <option key={choice} value={choice} disabled={!offered.includes(choice)}>
                {extendLabel(choice)}
              </option>
            ))}
          </NativeSelect>
        )}
      </Field>
      <Button
        type="submit"
        variant="outline"
        loading={extend.isPending}
        disabled={hours === undefined}
      >
        Extend
      </Button>
      {extend.isError ? (
        <p role="alert" className="w-full text-sm text-destructive">
          {extend.error.message}
        </p>
      ) : null}
    </form>
  );
}

function OpenForm({
  environment,
  onOpened,
}: {
  environment: string;
  onOpened: (next: SupportWindow) => void;
}) {
  const [level, setLevel] = useState<SupportLevel>("readOnly");
  const [hours, setHours] = useState(4);
  const [caseReference, setCaseReference] = useState("");
  const open = useMutation({
    mutationFn: () =>
      openSupportAccess(environment, { level, hours, caseReference: caseReference.trim() }),
    onSuccess: onOpened,
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    open.mutate();
  }

  return (
    <form className="space-y-5" onSubmit={submit} aria-label="Open support access">
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Access level</legend>
        {levels.map((choice) => (
          <label
            key={choice}
            className="flex cursor-pointer gap-3 rounded-lg border border-border p-3 has-[:checked]:border-primary has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-primary"
          >
            <input
              type="radio"
              name={`support-level-${environment}`}
              className="mt-1 accent-primary"
              checked={level === choice}
              onChange={() => setLevel(choice)}
            />
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">
                {levelName[choice]}
                {choice === "readOnly" ? (
                  <span className="font-normal text-muted-foreground"> (default)</span>
                ) : null}
              </span>
              <span className="block text-sm text-muted-foreground">{levelFacts[choice]}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <div className="grid gap-5 sm:grid-cols-2">
        <Field
          id={`support-hours-${environment}`}
          label="Duration"
          help="Access ends on its own when this runs out."
        >
          {(props) => (
            <NativeSelect
              {...props}
              value={hours}
              onChange={(event) => setHours(Number(event.target.value))}
            >
              {supportHours.map((choice) => (
                <option key={choice} value={choice}>
                  {hoursLabel(choice)}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>
        <Field
          id={`support-case-${environment}`}
          label="ServiceNow case number"
          help="Optional. The support engineer finds the case with it."
        >
          {(props) => (
            <Input
              {...props}
              autoComplete="off"
              spellCheck={false}
              className="font-mono sm:max-w-xs"
              value={caseReference}
              onChange={(event) => setCaseReference(event.target.value)}
            />
          )}
        </Field>
      </div>
      <p className="text-xs text-muted-foreground">{recordNote}</p>
      {open.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {open.error.message}
        </p>
      ) : null}
      <Button type="submit" loading={open.isPending}>
        Open support access
      </Button>
    </form>
  );
}
