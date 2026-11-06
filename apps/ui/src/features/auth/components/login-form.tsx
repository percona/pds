import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert } from "lucide-react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { loginNamePattern } from "../login-name";
import type { SignInInput } from "../types";

const schema = z.object({
  username: z
    .string()
    .trim()
    .min(3, "A username is at least 3 characters.")
    .max(63, "Use 63 characters or fewer.")
    .regex(loginNamePattern, "Use lowercase letters, numbers, and dashes. Start with a letter."),
  // No length rule on the way in. The password was chosen against the rules
  // once already, and refusing to send a short one would mean answering a
  // wrong password with advice about how to write it.
  password: z.string().min(1, "Enter your password."),
});

type LoginFormValues = z.infer<typeof schema>;

type LoginFormProps = {
  onSubmit: (input: SignInInput) => void;
  submitting?: boolean;
  error?: string | null;
  /** Pre-filled after a customer sets their password, so they type it once. */
  username?: string;
  /**
   * Says there is no sign up. It is true only where Percona creates the
   * login; with an identity provider anybody can create an organization.
   */
  noSignUp?: boolean;
};

/** The sign in form: username, password, one button. */
export function LoginForm({
  onSubmit,
  submitting,
  error,
  username = "",
  noSignUp = true,
}: LoginFormProps) {
  const {
    register,
    handleSubmit,
    formState: { errors, isValid },
  } = useForm<LoginFormValues>({
    resolver: zodResolver(schema),
    mode: "onTouched",
    defaultValues: { username, password: "" },
  });

  return (
    <form className="space-y-5" onSubmit={handleSubmit(onSubmit)} noValidate>
      <Field
        id="username"
        label="Username"
        help="The id Percona set up for your organization."
        error={errors.username?.message}
      >
        {(field) => (
          <Input
            {...field}
            {...register("username")}
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            className="font-mono"
          />
        )}
      </Field>

      <Field
        id="password"
        label="Password"
        help="The password you set from your invite link."
        error={errors.password?.message}
      >
        {(field) => (
          <Input
            {...field}
            {...register("password")}
            type="password"
            autoComplete="current-password"
          />
        )}
      </Field>

      {error ? (
        <p role="alert" className="flex items-start gap-2 text-sm text-destructive">
          <CircleAlert className="mt-0.5 size-4" strokeWidth={1.5} aria-hidden="true" />
          {error}
        </p>
      ) : null}

      <Button
        type="submit"
        className="w-full"
        disabled={!isValid || submitting}
        loading={submitting}
      >
        {submitting ? "Signing in..." : "Sign in"}
      </Button>

      {noSignUp ? (
        <p className="text-sm text-muted-foreground">
          Percona creates your organization and sends you an invite link. There is no sign up.
        </p>
      ) : null}
    </form>
  );
}
