import { CircleAlert } from "lucide-react";
import { useState } from "react";
import { useSearchParams } from "react-router";
import { Skeleton } from "@/components/ui/skeleton";
import { AuthCard } from "@/features/auth";
import { ConsentForm, ReturningStatus, useConsentRequest } from "@/features/agent-consent";
import { productName } from "@/lib/product";
import { visit } from "@/lib/visit";

/** Whether this page sits inside another one. A decision made behind a frame is not one a person saw. */
function isFramed(): boolean {
  try {
    return window.top !== window.self;
  } catch {
    return true;
  }
}

/**
 * Where an app that asked to sign in is allowed in, or turned away. The control
 * plane sends a signed in person here with a request token, and nothing is
 * decided until they press a button.
 *
 * The web server already refuses to let any page frame this one. Refusing to
 * render in a frame as well keeps a decision from being made behind one when a
 * proxy in front drops that header.
 */
export function AuthorizeAgentPage() {
  const [params] = useSearchParams();
  const request = params.get("request") ?? "";
  const framed = isFramed();
  const consent = useConsentRequest(request, !framed);
  const [leaving, setLeaving] = useState(false);

  if (framed) {
    return (
      <AuthCard
        title="Open this in its own window"
        description="This page must be opened in its own window."
      >
        <p className="text-sm text-muted-foreground">
          Signing in an app is only safe when you can see the whole page. Open the link from the app
          again in a new tab.
        </p>
      </AuthCard>
    );
  }

  if (request === "" || (consent.isError && consent.error.status === 404)) {
    return <Gone />;
  }

  return (
    <AuthCard
      wide
      title="Allow an app to use your account?"
      description={`An app is asking to act for you in ${productName}. Check who is asking before you allow it.`}
    >
      {consent.isPending ? (
        <div className="space-y-3" role="status" aria-label="Loading the request">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : consent.isError ? (
        <p role="alert" className="flex items-start gap-2 text-sm text-destructive">
          <CircleAlert className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
          {consent.error.message}
        </p>
      ) : leaving ? (
        <ReturningStatus />
      ) : (
        <ConsentForm
          request={request}
          consent={consent.data}
          onDecided={(url) => {
            setLeaving(true);
            visit(url);
          }}
        />
      )}
    </AuthCard>
  );
}

/** The request expired, was answered, or never belonged to this person. All three read the same. */
function Gone() {
  return (
    <AuthCard
      title="This request is no longer waiting"
      description="It may have expired, or someone already answered it."
    >
      <p className="text-sm text-muted-foreground">
        Go back to the app that sent you here and start the sign in again.
      </p>
    </AuthCard>
  );
}
