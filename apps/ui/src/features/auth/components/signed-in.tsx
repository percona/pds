import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleAlert, Loader2 } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { Navigate, useLocation } from "react-router";
import { Button } from "@/components/ui/button";
import { fetchFeatureFlags, installFeatureFlags } from "@/features/flags";
import { productName } from "@/lib/product";
import { onSignedOut } from "@/lib/signed-out";
import { flagsKey, forgetSignedInState, useSession } from "../hooks/use-session";

/** Two more tries a half second apart, so a blip at page load is not a failure. */
const flagReadRetries = 2;
const flagReadRetryDelayMs = 500;

/**
 * The gate around the product. Nothing below it renders until the adapter has
 * said who is signed in, so no screen ever asks for data it cannot have.
 * Nor does it render until the flag set is known: a read that keeps failing
 * stops here rather than showing surfaces the deployment turned off.
 *
 * The flag set is read here rather than at bootstrap, because it is behind the
 * same session as everything else. Reading it before sign in would answer 401
 * and leave every surface on, which is the wrong answer for a deployment that
 * turned one off.
 */
export function SignedIn({ children }: { children: ReactNode }) {
  const location = useLocation();
  const queryClient = useQueryClient();
  const session = useSession();
  const signedIn = session.data != null;

  const flags = useQuery({
    queryKey: flagsKey,
    queryFn: fetchFeatureFlags,
    enabled: signedIn,
    staleTime: Infinity,
    retry: flagReadRetries,
    retryDelay: flagReadRetryDelayMs,
  });

  // Installing is a side effect on a module, not state React can render from,
  // so the flag that says it happened is what gates the children.
  const [flagsInstalled, setFlagsInstalled] = useState(false);
  useEffect(() => {
    if (flags.data) {
      installFeatureFlags(flags.data);
      setFlagsInstalled(true);
    }
  }, [flags.data]);

  // A session can end while the tab is open. The request layer sees it first,
  // as a 401 on a route that worked, and this turns that into the login page
  // rather than a screen of failed reads. Everything cached goes with it, the
  // same as a sign out, so whoever signs in next never sees it.
  useEffect(() => onSignedOut(() => forgetSignedInState(queryClient)), [queryClient]);

  if (session.isPending) {
    return <AuthSplash />;
  }

  // An adapter that cannot be reached is not the same as being signed out, and
  // sending the customer to a login form that would also fail says the wrong
  // thing about what is wrong.
  if (session.isError) {
    return <AdapterUnreachable />;
  }

  if (!signedIn) {
    return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  }

  if (flags.isError) {
    return <FlagsUnavailable />;
  }

  if (!flagsInstalled) {
    return <AuthSplash />;
  }

  return children;
}

/** The blank moment between load and knowing who is signed in. */
function AuthSplash() {
  return (
    <div
      role="status"
      className="flex min-h-screen items-center justify-center gap-2 bg-background text-muted-foreground"
    >
      <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
      <span className="text-sm">Loading {productName}</span>
    </div>
  );
}

/** Shown when the control plane did not answer at all. */
function AdapterUnreachable() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-6">
      <div className="max-w-sm space-y-2 rounded-lg border border-border bg-card p-6">
        <p className="flex items-center gap-2 text-base font-semibold">
          <CircleAlert className="size-4 text-destructive" strokeWidth={1.5} aria-hidden="true" />
          The control plane could not be reached
        </p>
        <p className="text-sm text-muted-foreground">
          Signing in needs it. Check that it is running, then reload this page.
        </p>
      </div>
    </div>
  );
}

/** Shown when the flag set could not be read, even after retrying. */
function FlagsUnavailable() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-6">
      <div className="max-w-sm space-y-4 rounded-lg border border-border bg-card p-6">
        <div className="space-y-2">
          <p className="flex items-center gap-2 text-base font-semibold">
            <CircleAlert className="size-4 text-destructive" strokeWidth={1.5} aria-hidden="true" />
            The console could not load its settings
          </p>
          <p className="text-sm text-muted-foreground">Reload this page to try again.</p>
        </div>
        <Button onClick={() => window.location.reload()}>Reload</Button>
      </div>
    </div>
  );
}
