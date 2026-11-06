import { zodResolver } from "@hookform/resolvers/zod";
import { CircleAlert } from "lucide-react";
import { useForm } from "react-hook-form";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { loginNamePattern } from "../login-name";
import type { ClaimInput } from "../types";

/** The shortest password the adapter will store. */
export const minPasswordLength = 12;

/**
 * bcrypt reads 72 bytes, so a longer password would be cut without saying so.
 *
 * The adapter counts bytes and this counts characters, so a password of
 * multibyte characters can pass here and still be refused there. The adapter's
 * message names the same range, so the answer stays readable either way.
 */
export const maxPasswordLength = 72;

const loginRule = z
  .string()
  .trim()
  .min(3, "A login is at least 3 characters.")
  .max(63, "Use 63 characters or fewer.")
  .regex(loginNamePattern, "Use lowercase letters, numbers, and dashes. Start with a letter.");

/** The login is only asked for, and so only checked, when the form shows it. */
const claimSchema = (withLogin: boolean) =>
  z
    .object({
      login: withLogin ? loginRule : z.string(),
      password: z
        .string()
        .min(minPasswordLength, `Use at least ${minPasswordLength} characters.`)
        .max(maxPasswordLength, `Use ${maxPasswordLength} characters or fewer.`),
      confirm: z.string(),
    })
    .refine((values) => values.password === values.confirm, {
      path: ["confirm"],
      message: "Both passwords have to match.",
    });

type ClaimFormValues = z.infer<ReturnType<typeof claimSchema>>;

type ClaimFormProps = {
  /** Asks for the login a person invited to an Organization signs in with. */
  withLogin?: boolean;
  onSubmit: (values: Pick<ClaimInput, "password" | "login">) => void;
  submitting?: boolean;
  error?: string | null;
};

/**
 * The form behind an invite link. It asks twice, because a typo here is not
 * recoverable by the customer: the link is spent and only Percona can mint
 * another.
 */
export function ClaimForm({ withLogin = false, onSubmit, submitting, error }: ClaimFormProps) {
  const {
    register,
    handleSubmit,
    formState: { errors, isValid },
  } = useForm<ClaimFormValues>({
    resolver: zodResolver(claimSchema(withLogin)),
    mode: "onTouched",
    defaultValues: { login: "", password: "", confirm: "" },
  });

  return (
    <form
      className="space-y-5"
      onSubmit={handleSubmit((values) =>
        onSubmit(
          withLogin
            ? { login: values.login, password: values.password }
            : { password: values.password },
        ),
      )}
      noValidate
    >
      {withLogin ? (
        <Field
          id="login"
          label="Login"
          help="What you type to sign in. Lowercase letters, numbers and dashes."
          error={errors.login?.message}
        >
          {(field) => (
            <Input {...field} {...register("login")} autoComplete="username" spellCheck={false} />
          )}
        </Field>
      ) : null}

      <Field
        id="password"
        label="Password"
        help={`At least ${minPasswordLength} characters. Length beats punctuation.`}
        error={errors.password?.message}
      >
        {(field) => (
          <Input {...field} {...register("password")} type="password" autoComplete="new-password" />
        )}
      </Field>

      <Field
        id="confirm"
        label="Password again"
        help="This link works once, so a typo would need a new one."
        error={errors.confirm?.message}
      >
        {(field) => (
          <Input {...field} {...register("confirm")} type="password" autoComplete="new-password" />
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
        {submitting ? "Setting password..." : "Set password"}
      </Button>

      <p className="text-sm text-muted-foreground">
        Percona never sees this password. Keep it somewhere you can find it.
      </p>
    </form>
  );
}
