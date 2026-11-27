import { matchPath } from "react-router";
import type { FeatureFlagName } from "@/features/flags";

/** The surfaces a page with a back link, or its parent, sits behind. */
export type BackLinkSurface = Extract<
  FeatureFlagName,
  "databases" | "clusters" | "activity" | "support"
>;

export type ParentPage = {
  to: string;
  /** What the link says it goes back to, lower case: "Back to <label>". */
  label: string;
  /**
   * The surface the parent is gated by. A link to a turned off one is a not
   * found page. Absent for a parent no surface turns off, such as Teams.
   */
  surface?: BackLinkSurface;
};

/**
 * Pages with no parent: the sidebar entries, the root, the AI agents page, the Teams, Users and Settings screens, and the not found catch all, which has
 * nothing to sit under.
 */
export const topLevelPaths = [
  "/",
  "/environments",
  "/alerts",
  "/activity",
  "/support",
  "/agents",
  "/connectors",
  "/teams",
  "/users",
  "/settings",
  "/*",
];

type Rule = {
  pattern: string;
  /** The surface this page is gated by, the same one its route checks. Absent when none does. */
  surface?: BackLinkSurface;
  parent: (params: Record<string, string | undefined>) => ParentPage;
};

const environmentsList: ParentPage = {
  to: "/environments",
  label: "environments",
  surface: "clusters",
};

// The name is the segment as it sits in the address bar, still encoded, so it
// goes back into a path unchanged.
function environmentDatabases(name = ""): ParentPage {
  return { to: `/environments/${name}`, label: "databases", surface: "databases" };
}

const rules: Rule[] = [
  {
    pattern: "/databases/:environment/:id",
    surface: "databases",
    parent: ({ environment }) => environmentDatabases(environment),
  },
  {
    pattern: "/environments/new",
    surface: "clusters",
    parent: () => environmentsList,
  },
  {
    pattern: "/environments/aws-accounts/new",
    surface: "clusters",
    parent: () => environmentsList,
  },
  {
    pattern: "/environments/:name/new",
    surface: "databases",
    parent: ({ name }) => environmentDatabases(name),
  },
  // The environment is usually gone by the time anyone reads the cleanup guide,
  // so its own databases page would only say it is not on the list.
  {
    pattern: "/environments/:name/aws-cleanup",
    surface: "clusters",
    parent: () => environmentsList,
  },
  {
    pattern: "/environments/:name/openeverest",
    surface: "clusters",
    parent: () => environmentsList,
  },
  {
    pattern: "/environments/:name/update",
    surface: "clusters",
    parent: () => environmentsList,
  },
  {
    pattern: "/environments/:name",
    surface: "databases",
    parent: () => environmentsList,
  },
  {
    pattern: "/activity/:id",
    surface: "activity",
    parent: () => ({ to: "/activity", label: "activity", surface: "activity" }),
  },
  {
    pattern: "/support/:number",
    surface: "support",
    parent: () => ({ to: "/support", label: "support", surface: "support" }),
  },
  {
    pattern: "/teams/:name",
    parent: () => ({ to: "/teams", label: "teams" }),
  },
];

export type ParentPageOptions = {
  enabled: (surface: BackLinkSurface) => boolean;
};

/**
 * The page a back link on `pathname` goes to, worked out from the route alone
 * so it answers the same on a deep link or a fresh tab as it does mid visit.
 *
 * Null for a top level page, for a path the app does not route, and when this
 * page or its parent is turned off.
 */
export function parentPage(pathname: string, options: ParentPageOptions): ParentPage | null {
  const matched = rules
    .map((rule) => ({ rule, match: matchPath({ path: rule.pattern, end: true }, pathname) }))
    .find((item) => item.match !== null);
  const on = (surface?: BackLinkSurface) => surface === undefined || options.enabled(surface);
  if (!matched?.match || !on(matched.rule.surface)) {
    return null;
  }
  const parent = matched.rule.parent(matched.match.params);

  return on(parent.surface) ? parent : null;
}
