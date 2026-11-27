import { zodResolver } from "@hookform/resolvers/zod";
import { ChevronDown, ChevronRight, CircleAlert, TriangleAlert } from "lucide-react";
import { useId, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { Link } from "react-router";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { ProfilePicker, defaultProfileId, findProfile, resourceBounds } from "@/features/profiles";
import { ContactSupportDialog, publicSupportPortalUrl } from "@/features/support";
import { productName } from "@/lib/product";
import { cn } from "@/lib/utils";
import { allowsAnyAddress, defaultSourceRange, parseSourceRanges } from "../access";
import {
  engineChoicePhrase,
  engines,
  postgresqlNameLimit,
  postgresqlNameReason,
  visibleEngines,
} from "../engines";
import type { CreateDatabaseInput, DatabaseAccessChoice, DatabaseEngine } from "../types";
import { EngineIcon } from "./engine-icon";

// A DNS-1035 label: the engine names a Kubernetes Service after the database,
// and a Service name must start with a letter. The adapter checks it again;
// this only saves a round trip.
const databaseName = /^[a-z]([-a-z0-9]*[a-z0-9])?$/;

const databaseNameMessage =
  "Use lowercase letters, numbers, and dashes. Start with a letter and end with a letter or number.";

const accessChoices: ReadonlyArray<{ value: DatabaseAccessChoice; label: string }> = [
  { value: "private", label: "Private" },
  { value: "public", label: "Public" },
];

const schema = z
  .object({
    engine: z.enum(["mysql", "postgresql"]),
    profile: z.enum(["dev", "staging", "prod"]),
    name: z
      .string()
      .trim()
      .min(1, "Enter a name.")
      .max(63, "Use 63 characters or fewer.")
      .regex(databaseName, databaseNameMessage),
    cpu: boundedInteger("CPU", "cores", resourceBounds.cpu.min, resourceBounds.cpu.max),
    memory: boundedInteger("Memory", "GB", resourceBounds.memory.min, resourceBounds.memory.max),
    storage: boundedInteger(
      "Storage",
      "GB",
      resourceBounds.storage.min,
      resourceBounds.storage.max,
    ),
    access: z.enum(["private", "public"]),
    sourceRanges: z.string(),
  })
  .superRefine((values, ctx) => {
    if (values.engine === "postgresql" && values.name.length > postgresqlNameLimit) {
      ctx.addIssue({
        code: "custom",
        path: ["name"],
        message: `Use ${postgresqlNameLimit} characters or fewer for PostgreSQL. ${postgresqlNameReason}`,
      });
    }
    if (values.access !== "public") {
      return;
    }

    const parsed = parseSourceRanges(values.sourceRanges);
    const error =
      parsed.error ??
      (parsed.ranges.length === 0 ? "Enter at least one address or range." : undefined);
    if (error) {
      ctx.addIssue({ code: "custom", path: ["sourceRanges"], message: error });
    }
  });

type CreateDatabaseFormValues = z.infer<typeof schema>;

// Overrides arrive as text. An empty field means "keep the profile value", so
// it passes; anything else has to be a whole number inside the bounds.
function boundedInteger(field: string, unit: string, min: number, max: number) {
  const message = `${field} must be between ${min} and ${max} ${unit}.`;

  return z
    .string()
    .trim()
    .refine((value) => {
      if (value === "") {
        return true;
      }

      const parsed = Number(value);

      return Number.isInteger(parsed) && parsed >= min && parsed <= max;
    }, message);
}

type CreateDatabaseFormProps = {
  onSubmit: (input: CreateDatabaseInput) => void;
  submitting?: boolean;
  error?: string | null;
  /**
   * False when the adapter answered 501. The dialog still opens so the
   * pre-filled text can be copied to the public portal.
   */
  supportConnected?: boolean;
  supportPortalUrl?: string;
  /** The environment this database is created on. */
  environment: string;
};

/**
 * The create form: engine when more than one is offered, then profile, then
 * the one required field, then the overrides most creates never touch.
 *
 * The overrides are CPU, memory, and storage. There is no namespace field: the
 * adapter manages exactly one namespace and rejects any other value, and the
 * browser has no way to know which one it is.
 */
export function CreateDatabaseForm({
  onSubmit,
  submitting,
  error,
  supportConnected = true,
  supportPortalUrl = publicSupportPortalUrl,
  environment,
}: CreateDatabaseFormProps) {
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [supportOpen, setSupportOpen] = useState(false);
  const advancedId = useId();

  const {
    control,
    formState: { errors, isValid },
    getFieldState,
    handleSubmit,
    register,
    setValue,
    trigger,
    watch,
  } = useForm<CreateDatabaseFormValues>({
    resolver: zodResolver(schema),
    // onTouched shows an error only after a field has been left, and still
    // recomputes isValid on every keystroke so the submit button unlocks as
    // soon as the form is usable.
    mode: "onTouched",
    defaultValues: {
      engine: "mysql",
      profile: defaultProfileId,
      name: "",
      cpu: "",
      memory: "",
      storage: "",
      access: "private",
      sourceRanges: "",
    },
  });

  const engine = watch("engine");
  const profileId = watch("profile");
  const name = watch("name");
  const access = watch("access");
  const sourceRanges = watch("sourceRanges");
  const profile = findProfile(profileId);
  const accessId = useId();
  const parsedRanges = parseSourceRanges(sourceRanges).ranges;
  const openToAnyAddress = access === "public" && allowsAnyAddress(parsedRanges);

  return (
    <>
      <form
        className="space-y-8"
        noValidate
        onSubmit={handleSubmit((values) => onSubmit(toCreateInput(values, environment)))}
      >
        {visibleEngines.length > 1 ? (
          <fieldset className="space-y-3">
            <legend className="text-base font-semibold">Engine</legend>
            <p className="text-sm text-muted-foreground">
              {productName} can run either engine. Match the one your application already uses.
            </p>
            <Controller
              control={control}
              name="engine"
              render={({ field }) => (
                <EnginePicker
                  value={field.value}
                  onChange={(next) => {
                    field.onChange(next);
                    // The name's length limit depends on the engine, and
                    // onTouched only checks a field again when that field
                    // changes. A name the user has left is checked against the
                    // new engine; an untouched one still waits for its blur.
                    if (getFieldState("name").isTouched) {
                      void trigger("name");
                    }
                  }}
                  disabled={submitting}
                />
              )}
            />
            <p className="text-sm text-muted-foreground">
              Not sure which to pick?{" "}
              <button
                type="button"
                disabled={submitting}
                onClick={() => setSupportOpen(true)}
                className="rounded-md font-medium text-primary hover:underline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
              >
                Contact support
              </button>
            </p>
          </fieldset>
        ) : null}

        <fieldset className="space-y-3">
          <legend className="text-base font-semibold">Profile</legend>
          <Controller
            control={control}
            name="profile"
            render={({ field }) => (
              <ProfilePicker value={field.value} onChange={field.onChange} disabled={submitting} />
            )}
          />
        </fieldset>

        <Field
          id="database-name"
          label="Name"
          help="Apps resolve this as the hostname. It cannot be changed later."
          error={errors.name?.message}
        >
          {(props) => (
            <Input {...register("name")} {...props} autoComplete="off" disabled={submitting} />
          )}
        </Field>

        <fieldset className="space-y-3">
          <legend id={`${accessId}-legend`} className="text-base font-semibold">
            Access
          </legend>
          <div className="space-y-1.5">
            <div
              role="radiogroup"
              aria-labelledby={`${accessId}-legend`}
              aria-describedby={`${accessId}-help ${accessId}-choice`}
              className="inline-flex gap-1 rounded-md border border-input bg-muted p-0.5"
            >
              {accessChoices.map((choice) => (
                <label
                  key={choice.value}
                  className={cn(
                    "cursor-pointer rounded-md px-4 py-1.5 text-sm font-medium text-muted-foreground transition-colors",
                    "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
                    "has-[:checked]:bg-primary has-[:checked]:text-primary-foreground",
                    submitting && "cursor-not-allowed opacity-50",
                  )}
                >
                  <input
                    type="radio"
                    value={choice.value}
                    {...register("access", {
                      onChange: (event) => {
                        if (event.target.value === "public" && sourceRanges.trim() === "") {
                          setValue("sourceRanges", defaultSourceRange, {
                            shouldDirty: true,
                            shouldValidate: true,
                          });
                        }
                      },
                    })}
                    disabled={submitting}
                    className="sr-only"
                  />
                  {choice.label}
                </label>
              ))}
            </div>
            <p id={`${accessId}-choice`} className="text-sm">
              {access === "public"
                ? "Reachable from the internet."
                : "Reachable from your VPC and the networks connected to it."}
            </p>
            <p id={`${accessId}-help`} className="text-sm text-muted-foreground">
              Adds a load balancer in your cloud account, billed by your cloud provider.
            </p>
          </div>

          {access === "public" ? (
            <div className="space-y-3">
              <Field
                id="database-source-ranges"
                label="Allowed addresses"
                help="IP addresses or CIDR ranges, separated by commas. 0.0.0.0/0 allows any address."
                error={errors.sourceRanges?.message}
              >
                {(props) => (
                  <Input
                    {...register("sourceRanges")}
                    {...props}
                    autoComplete="off"
                    spellCheck={false}
                    className="font-mono"
                    disabled={submitting}
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

        <div className="space-y-4">
          <button
            type="button"
            aria-expanded={advancedOpen}
            aria-controls={advancedId}
            onClick={() => setAdvancedOpen((open) => !open)}
            className="flex items-center gap-1.5 rounded-md text-sm font-medium text-foreground"
          >
            {advancedOpen ? (
              <ChevronDown className="size-4" strokeWidth={1.5} aria-hidden="true" />
            ) : (
              <ChevronRight className="size-4" strokeWidth={1.5} aria-hidden="true" />
            )}
            Advanced
          </button>

          {advancedOpen ? (
            <div id={advancedId} className="grid gap-6 sm:grid-cols-2">
              <Field
                id="database-cpu"
                label="CPU cores"
                help={`${profile.label} uses ${profile.cpuCores === 1 ? "1 core" : `${profile.cpuCores} cores`}. Allowed ${resourceBounds.cpu.min} to ${resourceBounds.cpu.max}.`}
                error={errors.cpu?.message}
              >
                {(props) => (
                  <Input
                    {...register("cpu")}
                    {...props}
                    inputMode="numeric"
                    className="font-mono"
                    disabled={submitting}
                  />
                )}
              </Field>

              <Field
                id="database-memory"
                label="Memory in GB"
                help={`${profile.label} uses ${profile.memoryGb} GB. Allowed ${resourceBounds.memory.min} to ${resourceBounds.memory.max}.`}
                error={errors.memory?.message}
              >
                {(props) => (
                  <Input
                    {...register("memory")}
                    {...props}
                    inputMode="numeric"
                    className="font-mono"
                    disabled={submitting}
                  />
                )}
              </Field>

              <Field
                id="database-storage"
                label="Storage in GB"
                help={`${profile.label} uses ${profile.storageGb} GB. Allowed ${resourceBounds.storage.min} to ${resourceBounds.storage.max}.`}
                error={errors.storage?.message}
              >
                {(props) => (
                  <Input
                    {...register("storage")}
                    {...props}
                    inputMode="numeric"
                    className="font-mono"
                    disabled={submitting}
                  />
                )}
              </Field>
            </div>
          ) : null}
        </div>

        {error ? (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
          >
            <CircleAlert
              className="mt-0.5 size-4 shrink-0 text-destructive"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            <span>{error}</span>
          </div>
        ) : null}

        <div className="flex items-center gap-2">
          <Button type="submit" disabled={!isValid} loading={submitting}>
            {submitting ? "Creating..." : "Create database"}
          </Button>
          <Button variant="outline" asChild>
            <Link to={`/environments/${encodeURIComponent(environment)}`}>Cancel</Link>
          </Button>
        </div>
      </form>
      {visibleEngines.length > 1 ? (
        <ContactSupportDialog
          open={supportOpen}
          onOpenChange={setSupportOpen}
          subject="Help choosing a database engine"
          description={engineChoiceDescription({ engine, profile: profileId, name })}
          context={engineChoiceContext({ engine, profile: profileId, name })}
          connected={supportConnected}
          portalUrl={supportPortalUrl}
        />
      ) : null}
    </>
  );
}

// Only fields the developer actually set travel to the adapter: an empty
// override must not read as "zero cores".
function toCreateInput(values: CreateDatabaseFormValues, environment: string): CreateDatabaseInput {
  const input: CreateDatabaseInput = {
    name: values.name,
    engine: values.engine,
    profile: values.profile,
    environment,
  };

  if (values.cpu !== "") input.cpu = Number(values.cpu);
  if (values.memory !== "") input.memory = Number(values.memory);
  if (values.storage !== "") input.storage = Number(values.storage);

  input.access = values.access;
  if (values.access === "public") {
    input.sourceRanges = parseSourceRanges(values.sourceRanges).ranges;
  }

  return input;
}

function engineChoiceDescription(values: {
  engine: DatabaseEngine;
  profile: CreateDatabaseFormValues["profile"];
  name: string;
}): string {
  const engine = engines.find((item) => item.id === values.engine)?.label ?? values.engine;
  const profile = findProfile(values.profile).label;
  const name = values.name.trim() === "" ? "not set yet" : values.name.trim();

  return [
    `I am creating a database in ${productName} and I am not sure whether to pick ${engineChoicePhrase}.`,
    "",
    "What I have so far:",
    `Engine selected: ${engine}`,
    `Profile: ${profile}`,
    `Name: ${name}`,
    "",
    "Please recommend an engine.",
  ].join("\n");
}

function engineChoiceContext(values: {
  engine: DatabaseEngine;
  profile: CreateDatabaseFormValues["profile"];
  name: string;
}) {
  const name = values.name.trim();

  return {
    engine: values.engine,
    profile: values.profile,
    ...(name === "" ? {} : { database: name }),
  };
}

type EnginePickerProps = {
  value: DatabaseEngine;
  onChange: (engine: DatabaseEngine) => void;
  disabled?: boolean;
};

function EnginePicker({ value, onChange, disabled }: EnginePickerProps) {
  const groupId = useId();

  return (
    <div className="grid gap-3 sm:grid-cols-2">
      {visibleEngines.map((engine) => {
        const inputId = `${groupId}-${engine.id}`;
        const labelId = `${inputId}-label`;
        const detailId = `${inputId}-detail`;
        const checked = engine.id === value;

        return (
          <label
            key={engine.id}
            htmlFor={inputId}
            data-testid={`engine-card-${engine.id}`}
            className={cn(
              "flex cursor-pointer flex-col gap-1 rounded-lg border bg-card p-4 transition-colors",
              "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
              checked ? "border-primary" : "border-border hover:bg-accent",
              disabled && "cursor-not-allowed opacity-50",
            )}
          >
            <input
              id={inputId}
              type="radio"
              name={groupId}
              value={engine.id}
              checked={checked}
              disabled={disabled}
              onChange={() => onChange(engine.id)}
              aria-labelledby={labelId}
              aria-describedby={detailId}
              className="sr-only"
            />
            <span className="flex items-center gap-2">
              <EngineIcon engine={engine.id} className="size-4" />
              <span id={labelId} className="text-sm font-medium">
                {engine.label}
              </span>
            </span>
            <span id={detailId} className="text-sm text-muted-foreground">
              {engine.detail}
            </span>
          </label>
        );
      })}
    </div>
  );
}
