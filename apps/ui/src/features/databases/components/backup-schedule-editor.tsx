import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert } from "lucide-react";
import { useEffect, useId, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { useSetBackupSchedule } from "../hooks/use-set-backup-schedule";
import {
  localTimeZone,
  maxRetentionCopies,
  minRetentionCopies,
  scheduleFormFrom,
  scheduleRequestFrom,
  weekdayNames,
} from "../schedule";
import type { ScheduleFormValues } from "../schedule";
import type { Database } from "../types";

type BackupScheduleEditorProps = {
  database: Database;
  /** False when the environment is offline and the hub cannot carry the change. */
  enabled?: boolean;
  /** The viewer's zone. Tests pin it; the page leaves it to the browser. */
  timeZone?: string;
  /** The day the offset is read on. Tests pin it. */
  now?: Date;
};

const periods = [
  { value: "off", label: "Off" },
  { value: "hourly", label: "Hourly" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
] as const;

// One width for every control, so a row of selects and inputs lines up.
const controlWidth = "w-40";

// Monday first, the way most calendars read, with cron's numbering kept.
const weekdayOrder = [1, 2, 3, 4, 5, 6, 0];

/**
 * The backup schedule editor. It works in the viewer's clock and sends UTC,
 * opens on the schedule the database has, and sends the whole schedule on
 * save because the adapter replaces the one there rather than merging.
 */
export function BackupScheduleEditor({
  database,
  enabled = true,
  timeZone,
  now,
}: BackupScheduleEditorProps) {
  const fieldId = useId();
  const zone = timeZone ?? localTimeZone();
  const save = useSetBackupSchedule(database.id);

  // The schedule is compared by value: a poll that answers with the same one
  // is a new object, and resetting on it would throw away an edit in progress.
  const scheduleKey = JSON.stringify(database.backupSchedule ?? null);
  const defaultValues = useMemo(
    () => scheduleFormFrom(JSON.parse(scheduleKey) ?? undefined, zone, now),
    [scheduleKey, zone, now],
  );

  const {
    formState: { errors, isDirty, isValid },
    handleSubmit,
    register,
    reset,
    watch,
  } = useForm<ScheduleFormValues>({
    resolver: zodResolver(schema),
    mode: "onTouched",
    defaultValues,
  });

  useEffect(() => {
    reset(defaultValues);
  }, [defaultValues, reset]);

  const period = watch("period");
  const busy = save.isPending || !enabled;

  return (
    <form
      data-testid="backup-schedule-editor"
      className="space-y-4"
      noValidate
      onSubmit={handleSubmit((values) => {
        save.mutate(scheduleRequestFrom(values, zone, now), {
          onSuccess: () => {
            toast.success(
              values.period === "off" ? "Backup schedule turned off" : "Backup schedule saved",
            );
          },
        });
      })}
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field
          id={`${fieldId}-period`}
          label="Frequency"
          help={
            defaultValues.period === "custom"
              ? "Custom was set outside this page. Pick one to replace it."
              : "Off keeps backups already taken."
          }
          error={errors.period?.message}
        >
          {(props) => (
            <NativeSelect
              {...register("period")}
              {...props}
              className={controlWidth}
              disabled={busy}
            >
              {defaultValues.period === "custom" ? (
                <option value="custom" disabled>
                  Custom
                </option>
              ) : null}
              {periods.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>

        {period === "hourly" ? (
          <Field
            id={`${fieldId}-minute`}
            label="Minutes past the hour"
            help="0 to 59."
            error={errors.minute?.message}
          >
            {(props) => (
              <Input
                {...register("minute")}
                {...props}
                inputMode="numeric"
                className={cn("font-mono", controlWidth)}
                disabled={busy}
              />
            )}
          </Field>
        ) : null}

        {period === "weekly" ? (
          <Field id={`${fieldId}-weekday`} label="Day" help="In your time zone.">
            {(props) => (
              <NativeSelect
                {...register("weekday")}
                {...props}
                className={controlWidth}
                disabled={busy}
              >
                {weekdayOrder.map((day) => (
                  <option key={day} value={String(day)}>
                    {weekdayNames[day]}
                  </option>
                ))}
              </NativeSelect>
            )}
          </Field>
        ) : null}

        {period === "daily" || period === "weekly" ? (
          <Field
            id={`${fieldId}-time`}
            label="Time"
            help="In your time zone."
            error={errors.time?.message}
          >
            {(props) => (
              <Input
                {...register("time")}
                {...props}
                type="time"
                className={cn("font-mono", controlWidth)}
                disabled={busy}
              />
            )}
          </Field>
        ) : null}

        {period !== "off" ? (
          <Field
            id={`${fieldId}-copies`}
            label="Copies to keep"
            help={`${minRetentionCopies} to ${maxRetentionCopies}. The oldest goes first.`}
            error={errors.retentionCopies?.message}
          >
            {(props) => (
              <Input
                {...register("retentionCopies")}
                {...props}
                inputMode="numeric"
                className={cn("font-mono", controlWidth)}
                disabled={busy}
              />
            )}
          </Field>
        ) : null}
      </div>

      <p className="text-sm text-muted-foreground">
        Times are in your time zone, <span className="font-mono">{zone}</span>, and stored in UTC. A
        stored time can move by an hour when daylight saving starts or ends.
      </p>

      {save.error ? (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
        >
          <CircleAlert
            className="mt-0.5 size-4 shrink-0 text-destructive"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          <span>{save.error.message}</span>
        </div>
      ) : null}

      <Button
        type="submit"
        variant="outline"
        disabled={!enabled || !isDirty || !isValid}
        loading={save.isPending}
      >
        {save.isPending ? "Saving..." : "Save schedule"}
      </Button>
    </form>
  );
}

/**
 * The bounds mirror the adapter's, which refuses the same values. Only the
 * fields the chosen period uses are checked, since the others are not sent.
 */
const schema = z
  .object({
    period: z.enum(["off", "custom", "hourly", "daily", "weekly"]),
    minute: z.string().trim(),
    time: z.string().trim(),
    weekday: z.string(),
    retentionCopies: z.string().trim(),
  })
  .superRefine((values, ctx) => {
    if (values.period === "custom") {
      ctx.addIssue({ code: "custom", path: ["period"], message: "Pick a frequency." });

      return;
    }
    if (values.period === "off") {
      return;
    }
    if (values.period === "hourly" && !isIntegerBetween(values.minute, 0, 59)) {
      ctx.addIssue({
        code: "custom",
        path: ["minute"],
        message: "Minutes must be between 0 and 59.",
      });
    }
    if (values.period !== "hourly" && !/^([01]\d|2[0-3]):[0-5]\d$/.test(values.time)) {
      ctx.addIssue({ code: "custom", path: ["time"], message: "Pick a time." });
    }
    if (!isIntegerBetween(values.retentionCopies, minRetentionCopies, maxRetentionCopies)) {
      ctx.addIssue({
        code: "custom",
        path: ["retentionCopies"],
        message: `Copies must be between ${minRetentionCopies} and ${maxRetentionCopies}.`,
      });
    }
  });

function isIntegerBetween(value: string, min: number, max: number): boolean {
  const parsed = Number(value);

  return value !== "" && Number.isInteger(parsed) && parsed >= min && parsed <= max;
}
