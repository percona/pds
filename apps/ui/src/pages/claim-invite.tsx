import { CircleAlert, Loader2 } from "lucide-react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { Button } from "@/components/ui/button";
import {
  AuthCard,
  ClaimForm,
  ProviderLinks,
  useClaimInvite,
  useInvite,
  useSignInMethods,
} from "@/features/auth";
import type { ClaimInput, OrganizationInvite } from "@/features/auth";

type ClaimValues = Pick<ClaimInput, "password" | "login">;
import { ApiError } from "@/lib/api";

/**
 * The screen behind an invite link. It names what the link is for before
 * asking for anything, so a person can tell a link meant for them from one
 * that is not. A link from an organization admin joins an organization, and
 * one from Percona sets a username's password.
 *
 * Setting the password does not sign them in. They go to the sign in form with
 * their username filled in and use what they just chose, which is the only way
 * to find out it works while they are still on the page.
 */
export function ClaimInvitePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const token = params.get("token") ?? "";
  const invite = useInvite(token);
  const claim = useClaimInvite();

  // An adapter that did not answer says nothing about the link, so it must not
  // be reported as one that cannot be used. The customer would throw away a
  // link that is fine.
  if (invite.isError && !refusedTheLink(invite.error)) {
    return <Unreachable />;
  }

  if (token === "" || invite.isError) {
    return <UnusableLink message={linkMessage(invite.error)} />;
  }

  if (invite.isPending) {
    return (
      <AuthCard title="Set your password" description="Checking your invite link.">
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
          Checking the link
        </p>
      </AuthCard>
    );
  }

  const claimWith = (values: ClaimValues) => {
    claim.mutate(
      { token, ...values },
      {
        onSuccess: (session) => {
          void navigate(`/login?username=${encodeURIComponent(session.username)}`, {
            replace: true,
          });
        },
      },
    );
  };
  const claimError = claim.error == null ? null : linkMessage(claim.error);

  if ("organization" in invite.data) {
    return (
      <OrganizationInviteCard
        token={token}
        invite={invite.data}
        submitting={claim.isPending}
        error={claimError}
        onSubmit={claimWith}
      />
    );
  }

  return (
    <AuthCard
      title="Set your password"
      description={`This link sets the password for ${invite.data.username}.`}
    >
      <ClaimForm submitting={claim.isPending} error={claimError} onSubmit={claimWith} />
    </AuthCard>
  );
}

type OrganizationInviteCardProps = {
  token: string;
  invite: OrganizationInvite;
  submitting: boolean;
  error: string | null;
  onSubmit: (values: ClaimValues) => void;
};

/**
 * Joining an organization. With an identity provider the link rides through
 * the provider's sign in, which proves the address. Without one the person
 * picks a login and a password.
 */
function OrganizationInviteCard({
  token,
  invite,
  submitting,
  error,
  onSubmit,
}: OrganizationInviteCardProps) {
  const methods = useSignInMethods();
  const withProvider = methods.data?.includes("oidc") === true;

  return (
    <AuthCard
      title={`Join ${invite.organization}`}
      description={`You are invited as ${rolePhrase(invite.role)} in the ${invite.team} team.`}
    >
      <div className="space-y-5">
        <p className="text-sm text-muted-foreground">
          {invite.anyEmail ? (
            "Anyone with this link can join."
          ) : (
            <>
              Sent to <span className="font-mono text-foreground">{invite.email}</span>.
            </>
          )}
        </p>
        {methods.isPending ? (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
            Checking how to sign in
          </p>
        ) : withProvider ? (
          <ProviderLinks invite={token} />
        ) : (
          <ClaimForm withLogin submitting={submitting} error={error} onSubmit={onSubmit} />
        )}
      </div>
    </AuthCard>
  );
}

/**
 * Reports whether the adapter answered about the link at all. A refusal is a
 * 404 or a 409; a transport failure carries status 0, and a 5xx is the adapter
 * failing rather than the link.
 */
function refusedTheLink(error: unknown): boolean {
  return error instanceof ApiError && error.status >= 400 && error.status < 500;
}

/** Shown when the control plane did not answer, so the link is still unjudged. */
function Unreachable() {
  return (
    <AuthCard
      title="The control plane could not be reached"
      description="Your invite link may still be fine. Try again in a moment."
    >
      <p className="flex items-start gap-2 text-sm text-muted-foreground">
        <CircleAlert
          className="mt-0.5 size-4 text-destructive"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        Setting a password needs the control plane. Reload this page once it is back.
      </p>
    </AuthCard>
  );
}

/** What a spent, expired or mistyped link leads to. */
function UnusableLink({ message }: { message: string }) {
  return (
    <AuthCard title="This link cannot be used" description={message}>
      <div className="space-y-4">
        <p className="flex items-start gap-2 text-sm text-muted-foreground">
          <CircleAlert
            className="mt-0.5 size-4 text-destructive"
            strokeWidth={1.5}
            aria-hidden="true"
          />
          Ask for a new invite link. Each one works once.
        </p>
        <Button variant="outline" className="w-full" asChild>
          <Link to="/login">Sign in</Link>
        </Button>
      </div>
    </AuthCard>
  );
}

/**
 * The adapter already tells a spent link from an expired one from one that was
 * never issued, so its message is used rather than flattened into a guess.
 */
function linkMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.message;
  }

  return "That invite link is not valid.";
}

function rolePhrase(role: OrganizationInvite["role"]): string {
  return role === "admin" ? "an admin" : "a user";
}
