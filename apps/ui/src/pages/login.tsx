import { ChevronDown, ChevronUp, Loader2 } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, Navigate, useLocation, useNavigate, useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import { InlineError } from "@/components/ui/inline-error";
import {
  AuthCard,
  LoginForm,
  OrganizationNameField,
  isOrganizationNameReady,
  ProviderLinks,
  signInErrorMessage,
  useSession,
  useSignIn,
  useSignInMethods,
} from "@/features/auth";
import { agentSignInReturn, ReturningStatus } from "@/features/agent-consent";
import { ApiError } from "@/lib/api";
import { visit } from "@/lib/visit";
import { productName } from "@/lib/product";

/** Where a customer lands after signing in when nothing sent them here. */
const defaultDestination = "/";

/**
 * The sign in screen. A customer who was sent here from a page they asked for
 * goes back to it once they are in, so a bookmarked database still opens.
 *
 * With an identity provider the providers come first and the password form is
 * a disclosure, and the same card turns into signing up, which asks only for
 * the organization's name. Without one it is the password form alone.
 */
export function LoginPage() {
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const session = useSession();
  const signIn = useSignIn();
  const methods = useSignInMethods();

  // The claim page sends the username across, so a customer who has just set a
  // password does not type their id a second time.
  const suggested = params.get("username") ?? "";
  const from = (location.state as { from?: string } | null)?.from;
  const refusal = params.get("error");
  // An app that asked to sign in sent the person here. The way back leaves the
  // console, so it is a page load and only an authorization request qualifies.
  const agentReturn = agentSignInReturn(params.get("next"));
  const returnTo = (): void => {
    if (agentReturn !== null) {
      visit(agentReturn);

      return;
    }
    void navigate(from ?? defaultDestination, { replace: true });
  };

  // Somebody already signed in as this username, or who opened /login with no
  // target, has no business on this screen. A leftover session for somebody
  // else must not skip the form: that is how claiming a password in a
  // browser that was already signed in used to drop them back in as the
  // old username.
  if (session.data != null && (suggested === "" || suggested === session.data.username)) {
    if (agentReturn !== null) {
      return <ReturningToApp to={agentReturn} />;
    }

    return <Navigate to={defaultDestination} replace />;
  }

  if (methods.isPending) {
    return (
      <AuthCard title="Sign in" description={`Your ${productName} control plane.`}>
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
          Checking how to sign in
        </p>
      </AuthCard>
    );
  }

  // A read that failed leaves the password form: every control plane serves it.
  const hasProvider = methods.data?.includes("oidc") === true;
  const passwordForm = (
    <LoginForm
      username={suggested}
      noSignUp={!hasProvider}
      submitting={signIn.isPending}
      error={signInMessage(signIn.error)}
      onSubmit={(input) => {
        signIn.mutate(input, {
          onSuccess: returnTo,
        });
      }}
    />
  );

  if (!hasProvider) {
    return (
      <AuthCard title="Sign in" description={`Your ${productName} control plane.`}>
        {passwordForm}
      </AuthCard>
    );
  }

  const refused =
    refusal === null ? null : (
      <InlineError message={signInErrorMessage(refusal, params.get("invited"))} />
    );

  if (params.has("signup")) {
    return <SignUp refused={refused} />;
  }

  return (
    <AuthCard title="Sign in" description={`Your ${productName} control plane.`}>
      <div className="space-y-5">
        {refused}
        <ProviderLinks />
        <PasswordDisclosure defaultOpen={suggested !== ""}>{passwordForm}</PasswordDisclosure>
        <p className="text-sm text-muted-foreground">
          New here?{" "}
          <Link to="/login?signup" className="font-medium text-primary hover:underline">
            Create an organization
          </Link>
        </p>
      </div>
    </AuthCard>
  );
}

/**
 * Creating an organization: its name, then a provider. The name travels with
 * the provider's sign in and is used only if this is the person's first one.
 */
function SignUp({ refused }: { refused: React.ReactNode }) {
  const [name, setName] = useState("");
  const ready = isOrganizationNameReady(name);

  return (
    <AuthCard
      title="Create your organization"
      description="You become its admin, and you can invite your team afterwards."
    >
      <div className="space-y-5">
        {refused}
        <OrganizationNameField
          value={name}
          onChange={setName}
          help="Your company or team. You can create more later."
          autoFocus
        />
        <ProviderLinks organization={ready ? name : ""} />
        <p className="text-sm text-muted-foreground">
          Already have an account?{" "}
          <Link to="/login" className="font-medium text-primary hover:underline">
            Sign in
          </Link>
        </p>
      </div>
    </AuthCard>
  );
}

/** The password form, tucked away because most people use a provider. */
function PasswordDisclosure({
  defaultOpen,
  children,
}: {
  defaultOpen: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const Chevron = open ? ChevronUp : ChevronDown;

  return (
    <div className="space-y-4 border-t border-border pt-4">
      <Button
        type="button"
        variant="ghost"
        size="sm"
        aria-expanded={open}
        aria-controls="password-sign-in"
        onClick={() => setOpen((value) => !value)}
      >
        Use a password
        <Chevron className="size-4" strokeWidth={1.5} aria-hidden="true" />
      </Button>
      {open ? <div id="password-sign-in">{children}</div> : null}
    </div>
  );
}

/**
 * The sentence under the form. The adapter's own message is used as it stands
 * for a refused sign in, because it says one thing about both halves of the
 * form on purpose.
 */
function signInMessage(error: unknown): string | null {
  if (error == null) {
    return null;
  }
  if (error instanceof ApiError) {
    return error.message;
  }

  return "Signing in did not finish. Try again.";
}

function ReturningToApp({ to }: { to: string }) {
  useEffect(() => visit(to), [to]);

  return (
    <AuthCard title="Sign in" description={`Your ${productName} control plane.`}>
      <ReturningStatus />
    </AuthCard>
  );
}
