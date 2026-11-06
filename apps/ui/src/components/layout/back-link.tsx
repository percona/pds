import { ChevronLeft } from "lucide-react";
import { Link, useLocation } from "react-router";
import { useFeature } from "@/features/flags";
import { parentPage, type BackLinkSurface } from "./parent-page";

/**
 * The link above every page that is not a sidebar entry. It goes to the
 * page's parent rather than back in history, which a deep link does not have.
 */
export function BackLink() {
  const { pathname } = useLocation();
  const surfaces: Record<BackLinkSurface, boolean> = {
    databases: useFeature("databases"),
    clusters: useFeature("clusters"),
    activity: useFeature("activity"),
    support: useFeature("support"),
  };
  const parent = parentPage(pathname, {
    enabled: (surface) => surfaces[surface],
  });

  if (!parent) {
    return null;
  }

  return (
    <div className="mb-6">
      <Link
        to={parent.to}
        className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 text-sm text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        <ChevronLeft className="size-4" strokeWidth={1.5} aria-hidden="true" />
        Back to {parent.label}
      </Link>
    </div>
  );
}
