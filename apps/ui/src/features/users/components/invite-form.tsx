import { zodResolver } from "@hookform/resolvers/zod";
import { useEffect, useId } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { InlineError } from "@/components/ui/inline-error";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import type { Role } from "@/features/auth";
import { useTeams } from "@/features/teams";
import { useCreateInvitation } from "../hooks/use-users";
import { roleLabel } from "../role-label";
import type { IssuedInvitation } from "../types";

const emailShape = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

const roles: Role[] = ["user", "admin"];

const schema = z
  .object({
    email: z.string().trim(),
    anyEmail: z.boolean(),
    team: z.string().min(1),
    role: z.enum(["user", "admin"]),
  })
  .refine((values) => values.anyEmail || emailShape.test(values.email), {
    path: ["email"],
    message: "Enter one email address.",
  });

type InviteFormValues = z.infer<typeof schema>;

type InviteFormProps = {
  onIssued: (invitation: IssuedInvitation) => void;
};

/**
 * Invites a person into a Team with a role. Any email lets whoever holds the
 * link join, and the control plane only allows that for the user role, so
 * checking it puts the role back to user and fixes it there.
 */
export function InviteForm({ onIssued }: InviteFormProps) {
  const fieldId = useId();
  const teams = useTeams();
  const invite = useCreateInvitation();
  const {
    register,
    handleSubmit,
    watch,
    setValue,
    resetField,
    formState: { errors, isValid },
  } = useForm<InviteFormValues>({
    resolver: zodResolver(schema),
    mode: "onTouched",
    defaultValues: { email: "", anyEmail: false, team: "", role: "user" },
  });
  const anyEmail = watch("anyEmail");
  const team = watch("team");
  const firstTeam = teams.data?.[0]?.name;

  // The Team select starts on the first Team once they are read.
  useEffect(() => {
    if (team === "" && firstTeam !== undefined) {
      setValue("team", firstTeam, { shouldValidate: true });
    }
  }, [team, firstTeam, setValue]);

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={handleSubmit((values) => {
        invite.mutate(
          { ...values, email: values.anyEmail ? "" : values.email },
          {
            onSuccess: (issued) => {
              onIssued(issued);
              resetField("email");
            },
          },
        );
      })}
    >
      <div className="grid gap-4 md:grid-cols-3">
        <Field
          id={`${fieldId}-email`}
          label="Email"
          help="Where the invite is sent."
          error={anyEmail ? undefined : errors.email?.message}
        >
          {(props) => (
            <Input
              {...props}
              {...register("email")}
              type="email"
              autoComplete="off"
              disabled={anyEmail || invite.isPending}
            />
          )}
        </Field>

        <Field id={`${fieldId}-team`} label="Team" help="Where they land.">
          {(props) => (
            <NativeSelect
              {...props}
              {...register("team")}
              className="w-full"
              disabled={invite.isPending}
            >
              {teams.data?.map(({ name }) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>

        <Field id={`${fieldId}-role`} label="Role" help="What they can do.">
          {(props) => (
            <NativeSelect
              {...props}
              {...register("role")}
              className="w-full"
              disabled={anyEmail || invite.isPending}
            >
              {roles.map((role) => (
                <option key={role} value={role}>
                  {roleLabel(role)}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>
      </div>

      <div className="flex items-start gap-2">
        <input
          id={`${fieldId}-any`}
          type="checkbox"
          className="mt-0.5 size-4 accent-primary"
          disabled={invite.isPending}
          {...register("anyEmail", {
            onChange: (event: { target: { checked: boolean } }) => {
              if (event.target.checked) {
                setValue("role", "user");
              }
            },
          })}
        />
        <div className="space-y-0.5">
          <Label htmlFor={`${fieldId}-any`}>Anyone with the link can join</Label>
          <p className="text-sm text-muted-foreground">
            No address is checked, and they join as a user.
          </p>
        </div>
      </div>

      <InlineError message={invite.error?.message} />

      <Button type="submit" disabled={!isValid} loading={invite.isPending}>
        {invite.isPending ? "Sending..." : "Send invite"}
      </Button>
    </form>
  );
}
