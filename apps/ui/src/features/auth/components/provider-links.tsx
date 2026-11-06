import { Button } from "@/components/ui/button";
import type { Provider } from "../types";

const providers: Array<{ provider: Provider; label: string }> = [
  { provider: "google", label: "Continue with Google" },
  { provider: "github", label: "Continue with GitHub" },
];

type ProviderLinksProps = {
  /**
   * The name for the Organization a first sign in creates. A sign up passes
   * it, a plain sign in leaves it out, and an empty string means the name is
   * not ready, so the providers are off.
   */
  organization?: string;
  /** The token of an invite link, which rides through the sign in and is claimed at the end of it. */
  invite?: string;
};

/**
 * One way in per identity provider. Each is a plain link to the control
 * plane, because the sign in is a trip through the provider's own pages and
 * not a request this page makes.
 */
export function ProviderLinks({ organization, invite }: ProviderLinksProps) {
  const waiting = organization !== undefined && organization.trim() === "";

  return (
    <div className="space-y-2">
      {providers.map(({ provider, label }) =>
        waiting ? (
          <Button key={provider} variant="outline" className="w-full" disabled>
            {label}
          </Button>
        ) : (
          <Button key={provider} variant="outline" className="w-full" asChild>
            <a href={startPath(provider, organization, invite)}>{label}</a>
          </Button>
        ),
      )}
    </div>
  );
}

function startPath(provider: Provider, organization?: string, invite?: string): string {
  let path = `/api/auth/oidc/start?provider=${provider}`;
  if (organization !== undefined) {
    path += `&organization=${encodeURIComponent(organization.trim())}`;
  }
  if (invite !== undefined) {
    path += `&invite=${encodeURIComponent(invite)}`;
  }

  return path;
}
