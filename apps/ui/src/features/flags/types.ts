/**
 * Every product surface that can be turned on or off, in the order the adapter
 * publishes them. The adapter owns these names; a flag added here without its
 * twin in the adapter reads as on and gates nothing.
 */
export const featureFlagNames = [
  "databases",
  "clusters",
  "backups",
  "connections",
  "support",
  "support-access",
  "chat",
  "assistant",
  "metrics",
  "activity",
  "mcp",
] as const;

export type FeatureFlagName = (typeof featureFlagNames)[number];

export type FeatureFlags = Record<FeatureFlagName, boolean>;

/**
 * The whole product. This is the answer whenever the adapter has nothing to
 * say about flags, so a control plane that cannot reach its own flag route
 * still shows every screen it used to.
 */
export function allFeaturesOn(): FeatureFlags {
  return Object.fromEntries(featureFlagNames.map((name) => [name, true])) as FeatureFlags;
}

/** isFeatureFlagName narrows a name the adapter sent to one the UI knows. */
export function isFeatureFlagName(name: string): name is FeatureFlagName {
  return (featureFlagNames as readonly string[]).includes(name);
}
