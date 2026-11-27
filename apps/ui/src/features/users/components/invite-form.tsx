import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert } from "lucide-react";
import { useEffect, useId } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import type { Role } from "@/features/auth";
import { useTeams } from "@/features/teams";
import { cn } from "@/lib/utils";
import { useSendInvitations } from "../hooks/use-users";
import { roleLabel } from "../role-label";
import type { InviteOutcome } from "../types";

const emailShape = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

const roles: Role[] = ["user", "admin"];

/** The addresses typed, split on commas, semicolons, spaces and new lines, each once. */
export function parseAddresses(text: string): string[] {
  const seen = new Set<string>();

  return text
    .split(/[\s,;]+/)
    .filter((address) => address !== "")
    .filter((address) => {
      const key = address.toLowerCase();
      if (seen.has(key)) {
        return false;
      }
      seen.add(key);

      return true;
    });
}

const schema = z
  .object({
    emails: z.string(),
    anyEmail: z.boolean(),
    team: z.string().min(1),
    role: z.enum(["user", "admin"]),
  })
  .superRefine((values, context) => {
    if (values.anyEmail) {
      return;
    }
    const addresses = parseAddresses(values.emails);
    const wrong = addresses.filter((address) => !emailShape.test(address));
    const message =
      addresses.length === 0
        ? "Enter at least one email address."
        : wrong.length === 1
          ? `${wrong[0]} is not an email address.`
          : wrong.length > 1
            ? `${wrong.length} entries are not email addresses.`
            : undefined;
    if (message !== undefined) {
      context.addIssue({ code: "custom", path: ["emails"], message });
    }
  });

type Refusal = Extract<InviteOutcome, { kind: "refused" }>;

function isRefusal(outcome: InviteOutcome): outcome is Refusal {
  return outcome.kind === "refused";
}

type InviteFormValues = z.infer<typeof schema>;

type InviteFormProps = {
  /** The Team the form starts on. The first Team when it is left out. */
  team?: string;
  onSent: (outcomes: InviteOutcome[]) => void;
};

/**
 * Invites one or several people into a Team with a role, one invitation per
 * address. Any email lets whoever holds the link join, and the control plane
 * only allows that for the user role, so checking it puts the role back to
 * user and fixes it there.
 *
 * It validates on change rather than on blur. On blur, leaving the empty
 * address box for the checkbox put an error under the box mid click, which
 * pushed the checkbox down before the click landed.
 *
 * When every address is refused the form stays open with each reason, so the
 * addresses can be fixed. Otherwise the outcomes go to the page, which lists
 * the links and any refusals.
 */
export function InviteForm({ team: startTeam, onSent }: InviteFormProps) {
  const fieldId = useId();
  const teams = useTeams();
  const send = useSendInvitations();
  const {
    register,
    handleSubmit,
    watch,
    setValue,
    formState: { errors, isValid },
  } = useForm<InviteFormValues>({
    resolver: zodResolver(schema),
    mode: "onChange",
    defaultValues: { emails: "", anyEmail: false, team: startTeam ?? "", role: "user" },
  });
  const anyEmail = watch("anyEmail");
  const team = watch("team");
  const role = watch("role");
  const addresses = anyEmail ? [] : parseAddresses(watch("emails"));
  const firstTeam = teams.data?.[0]?.name;

  // The Team select starts on the first Team once they are read.
  useEffect(() => {
    if (team === "" && firstTeam !== undefined) {
      setValue("team", firstTeam, { shouldValidate: true });
    }
  }, [team, firstTeam, setValue]);

  const refusals = send.data?.filter(isRefusal) ?? [];
  // A refusal stays in the form only when nothing was sent; otherwise the page lists it.
  const refused = refusals.length === send.data?.length ? refusals : [];
  const valid = addresses.filter((address) => emailShape.test(address)).length;
  const label = anyEmail ? "Create link" : valid > 1 ? `Send ${valid} invites` : "Send invite";

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={handleSubmit((values) => {
        const inputs = values.anyEmail
          ? [{ email: "", anyEmail: true, team: values.team, role: "user" as const }]
          : parseAddresses(values.emails).map((email) => ({
              email,
              anyEmail: false,
              team: values.team,
              role: values.role,
            }));
        send.mutate(inputs, {
          onSuccess: (outcomes) => {
            if (outcomes.some((outcome) => outcome.kind === "issued")) {
              onSent(outcomes);
            }
          },
        });
      })}
    >
      <Field
        id={`${fieldId}-emails`}
        label="Email addresses"
        help="One or several, separated by commas, spaces or new lines. Each address gets its own invite and link."
        error={anyEmail ? undefined : errors.emails?.message}
      >
        {(props) => (
          <textarea
            {...props}
            {...register("emails")}
            rows={3}
            autoComplete="off"
            spellCheck={false}
            disabled={anyEmail || send.isPending}
            className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-sm text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive"
          />
        )}
      </Field>

      {addresses.length > 0 ? (
        <ul aria-label="Addresses to invite" className="flex flex-wrap gap-1.5">
          {addresses.map((address) => {
            const ok = emailShape.test(address);

            return (
              <li
                key={address}
                className={cn(
                  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-xs",
                  ok ? "border-border" : "border-destructive text-destructive",
                )}
              >
                {ok ? null : (
                  <CircleAlert className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
                )}
                {address}
                {ok ? null : <span className="sr-only">, not an email address</span>}
              </li>
            );
          })}
        </ul>
      ) : null}

      <div className="flex items-start gap-2">
        <input
          id={`${fieldId}-any`}
          type="checkbox"
          className="mt-0.5 size-4 accent-primary"
          disabled={send.isPending}
          checked={anyEmail}
          onChange={(event) => {
            const next = event.target.checked;
            setValue("anyEmail", next, { shouldValidate: true });
            if (next) {
              setValue("role", "user", { shouldValidate: true });
            }
          }}
        />
        <div className="space-y-0.5">
          <Label htmlFor={`${fieldId}-any`}>Anyone with the link can join</Label>
          <p className="text-sm text-muted-foreground">
            No address is checked, and they join as a user.
          </p>
        </div>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field id={`${fieldId}-team`} label="Team" help="Where they land.">
          {(props) => (
            <NativeSelect
              {...props}
              {...register("team")}
              className="w-full"
              disabled={send.isPending}
            >
              {teams.data?.map(({ name }) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>

        <Field
          id={`${fieldId}-role`}
          label="Role"
          help={
            role === "admin"
              ? "Also manages the organization, its teams and its users."
              : "Works with the environments and databases of the team."
          }
        >
          {(props) => (
            <NativeSelect
              {...props}
              {...register("role")}
              className="w-full"
              disabled={anyEmail || send.isPending}
            >
              {roles.map((option) => (
                <option key={option} value={option}>
                  {roleLabel(option)}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>
      </div>

      {refused.length > 0 ? (
        <div role="alert" className="space-y-1 text-sm text-destructive">
          {refused.map((outcome) => (
            <p key={outcome.email} className="flex items-start gap-2">
              <CircleAlert
                className="mt-0.5 size-4 shrink-0"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <span>
                {outcome.email === "" ? null : (
                  <>
                    <span className="font-mono">{outcome.email}</span>.{" "}
                  </>
                )}
                {outcome.reason}
              </span>
            </p>
          ))}
        </div>
      ) : null}

      <Button type="submit" disabled={!isValid} loading={send.isPending}>
        {send.isPending ? "Sending..." : label}
      </Button>
    </form>
  );
}
