import { useQuery } from "@tanstack/react-query";
import { CircleAlert } from "lucide-react";
import { useId, type ReactNode } from "react";
import { useParams, useSearchParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Card } from "@/components/ui/card";
import { fetchConnectGuide } from "./api";
import { isEnvironmentName } from "./connect-commands";
import { joinDatabaseNamespace } from "./aws-cleanup";

export type GuideTarget = {
  name: string;
  namespace: string;
  region: string;
  /** The control plane's public address, or empty until the connect guide has answered or when it has none. */
  hubUrl: string;
  regionUnknown: boolean;
  /** True until the connect guide has answered or failed, while region and namespace are still fallbacks. */
  pending: boolean;
  /** Why no commands are shown, when the link carries a name or namespace that is not a DNS label. */
  problem?: string;
};

/**
 * What a command guide needs from its address: the environment, its database
 * namespace and the region its join defaulted to. Guides render from the
 * route alone because the environment is usually gone from the environments list by
 * the time anyone reads one.
 */
export function useGuideTarget(): GuideTarget {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const guide = useQuery({
    queryKey: ["environments", "connect-guide"],
    queryFn: ({ signal }) => fetchConnectGuide(signal),
  });
  const namespace =
    params.get("namespace")?.trim() || guide.data?.namespace?.trim() || joinDatabaseNamespace;

  // Both values come from the address bar and land in commands the reader
  // pastes into a shell, so a crafted link could otherwise hand them one.
  // Anything that is not a DNS label is refused rather than quoted.
  const problem = !isEnvironmentName(name)
    ? "The name in this link is not an environment name, so no commands are shown."
    : !isEnvironmentName(namespace)
      ? "The namespace in this link is not a namespace, so no commands are shown."
      : undefined;

  return {
    name,
    namespace,
    region: guide.data?.clusterRegion?.trim() || "us-west-2",
    hubUrl: guide.data?.hubUrl?.trim() || "",
    regionUnknown: guide.isError,
    pending: guide.isPending,
    problem,
  };
}

export function GuideRefusal({ title, problem }: { title: string; problem: string }) {
  return (
    <div className="space-y-6">
      <PageHeader title={title} />
      <Card className="flex items-center gap-3 px-5 py-5">
        <CircleAlert className="size-4 text-destructive" strokeWidth={1.5} aria-hidden="true" />
        <p className="text-sm">{problem}</p>
      </Card>
    </div>
  );
}

export function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono">{children}</span>;
}

export function Step({ n, title, children }: { n?: number; title: string; children: ReactNode }) {
  const id = useId();

  return (
    <section aria-labelledby={id}>
      <Card className="space-y-4 px-5 py-5">
        <h2 id={id} className="text-base font-semibold">
          {n ? `${n}. ${title}` : title}
        </h2>
        <div className="space-y-4">{children}</div>
      </Card>
    </section>
  );
}
