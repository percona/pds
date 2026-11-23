import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert } from "lucide-react";
import { useEffect, useId, useMemo } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
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
import { Label } from "@/components/ui/label";
import { resourceBounds } from "@/features/profiles";
import { cn } from "@/lib/utils";
import { useScaleDatabase } from "../hooks/use-scale-database";
import { memberChoices, memberConsequence, removedMembersNote, scaleChanges } from "../scale";
import type { ScaleField, ScaleValues } from "../scale";
import type { Database } from "../types";

type ScaleDialogProps = {
  database: Database;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /**
   * The field to open on, which is how an alert or a chat action pre-selects
   * the change: "Scale storage" lands on storage.
   */
  focusField?: ScaleField;
};

/**
 * The scale dialog. It opens on the current sizing so the developer edits a
 * number instead of restating one, refuses a storage shrink and a member count
 * without quorum before the round trip, and sends only the fields that moved.
 */
export function ScaleDialog({
  database,
  open,
  onOpenChange,
  focusField = "cpu",
}: ScaleDialogProps) {
  const fieldId = useId();
  const scale = useScaleDatabase(database.id);

  const current = useMemo<ScaleValues>(
    () => ({
      cpu: database.cpu,
      memory: database.memoryGb,
      storage: database.storageGb,
      replicas: database.members,
    }),
    [database.cpu, database.memoryGb, database.storageGb, database.members],
  );

  const schema = useMemo(() => buildSchema(current.storage), [current.storage]);
  const defaultValues = useMemo(() => toFormValues(current), [current]);

  const {
    formState: { errors, isDirty, isValid },
    handleSubmit,
    register,
    reset,
    setFocus,
    watch,
  } = useForm<ScaleFormValues>({
    resolver: zodResolver(schema),
    mode: "onTouched",
    defaultValues,
  });

  // Reopening on a database that has since changed has to show that database,
  // and a rejected attempt must not leave its error over a fresh form.
  useEffect(() => {
    if (open) {
      reset(defaultValues);
      scale.reset();
    }
  }, [open, defaultValues, reset, scale.reset]);

  const members = Number(watch("replicas"));
  const removedNote = removedMembersNote(database.engine, current.replicas, members);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-lg"
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          setFocus(focusField);
        }}
      >
        <DialogHeader>
          <DialogTitle>Scale {database.name}</DialogTitle>
          <DialogDescription>
            Members restart one at a time to apply this. Storage can only grow.
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={handleSubmit((values) => {
            const changes = scaleChanges(current, toScaleValues(values));

            scale.mutate(changes, {
              onSuccess: () => {
                toast.success("Scale requested");
                onOpenChange(false);
              },
            });
          })}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              id={`${fieldId}-cpu`}
              label="CPU cores"
              help={`Allowed ${resourceBounds.cpu.min} to ${resourceBounds.cpu.max}.`}
              error={errors.cpu?.message}
            >
              {(props) => (
                <Input
                  {...register("cpu")}
                  {...props}
                  inputMode="numeric"
                  className="font-mono"
                  disabled={scale.isPending}
                />
              )}
            </Field>

            <Field
              id={`${fieldId}-memory`}
              label="Memory in GB"
              help={`Allowed ${resourceBounds.memory.min} to ${resourceBounds.memory.max}.`}
              error={errors.memory?.message}
            >
              {(props) => (
                <Input
                  {...register("memory")}
                  {...props}
                  inputMode="numeric"
                  className="font-mono"
                  disabled={scale.isPending}
                />
              )}
            </Field>
          </div>

          <Field
            id={`${fieldId}-storage`}
            label="Storage in GB"
            help={`Now ${current.storage} GB. A volume can grow up to ${resourceBounds.storage.max}.`}
            error={errors.storage?.message}
          >
            {(props) => (
              <Input
                {...register("storage")}
                {...props}
                inputMode="numeric"
                className="font-mono"
                disabled={scale.isPending}
              />
            )}
          </Field>

          <fieldset className="space-y-1.5">
            <legend className="text-sm font-medium">Members</legend>
            <p className="text-sm text-muted-foreground">{memberConsequence(members)}</p>
            {removedNote ? <p className="text-sm text-muted-foreground">{removedNote}</p> : null}
            <div className="flex gap-2">
              {memberChoices.map((count) => (
                <Label
                  key={count}
                  htmlFor={`${fieldId}-replicas-${count}`}
                  className={cn(
                    "flex h-9 flex-1 cursor-pointer items-center justify-center rounded-md border font-mono text-sm",
                    "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
                    count === members ? "border-primary" : "border-input hover:bg-accent",
                    scale.isPending && "cursor-not-allowed opacity-50",
                  )}
                >
                  <input
                    {...register("replicas")}
                    id={`${fieldId}-replicas-${count}`}
                    type="radio"
                    value={count}
                    disabled={scale.isPending}
                    aria-label={count === 1 ? "1 member" : `${count} members`}
                    className="sr-only"
                  />
                  {count}
                </Label>
              ))}
            </div>
          </fieldset>

          {scale.error ? (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
            >
              <CircleAlert
                className="mt-0.5 size-4 shrink-0 text-destructive"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <span>{scale.error.message}</span>
            </div>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={scale.isPending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!isDirty || !isValid} loading={scale.isPending}>
              {scale.isPending ? "Scaling..." : "Scale"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

type ScaleFormValues = {
  cpu: string;
  memory: string;
  storage: string;
  replicas: string;
};

/**
 * The bounds are mirrored from the engine plugin, and the storage floor is the
 * current size: a PVC cannot shrink in place, so the adapter refuses it with
 * this same sentence.
 */
function buildSchema(currentStorageGb: number) {
  return z.object({
    cpu: boundedInteger("CPU", "cores", resourceBounds.cpu.min, resourceBounds.cpu.max),
    memory: boundedInteger("Memory", "GB", resourceBounds.memory.min, resourceBounds.memory.max),
    storage: boundedInteger(
      "Storage",
      "GB",
      resourceBounds.storage.min,
      resourceBounds.storage.max,
    ).refine((value) => Number(value) >= currentStorageGb, "Storage can only be increased."),
    replicas: z
      .string()
      .refine((value) => memberChoices.includes(Number(value)), "Members must be 1, 3, or 5."),
  });
}

// Every field is required here: the dialog opens on a value, so a blank field
// is an edit that is not finished rather than "leave this alone".
function boundedInteger(field: string, unit: string, min: number, max: number) {
  const message = `${field} must be between ${min} and ${max} ${unit}.`;

  return z
    .string()
    .trim()
    .refine((value) => {
      const parsed = Number(value);

      return value !== "" && Number.isInteger(parsed) && parsed >= min && parsed <= max;
    }, message);
}

function toFormValues(values: ScaleValues): ScaleFormValues {
  return {
    cpu: String(values.cpu),
    memory: String(values.memory),
    storage: String(values.storage),
    replicas: String(values.replicas),
  };
}

function toScaleValues(values: ScaleFormValues): ScaleValues {
  return {
    cpu: Number(values.cpu),
    memory: Number(values.memory),
    storage: Number(values.storage),
    replicas: Number(values.replicas),
  };
}
