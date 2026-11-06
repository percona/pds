import { OpenFeatureProvider } from "@openfeature/react-sdk";
import { OpenFeature, TypedInMemoryProvider } from "@openfeature/web-sdk";
import type { ReactNode } from "react";
import { allFeaturesOn, featureFlagNames, type FeatureFlags } from "./types";

/**
 * The OpenFeature domain every flag in this app is read under. Naming it keeps
 * the product's flags off the default client, so anything else that installs a
 * provider later cannot answer for a surface.
 */
export const featureFlagDomain = "pds";

/** One surface, as OpenFeature stores it: two variants and the one in force. */
type SurfaceFlag = {
  variants: { on: true; off: false };
  defaultVariant: "on" | "off";
  disabled: boolean;
};

function surfaceFlag(on: boolean): SurfaceFlag {
  return {
    variants: { on: true, off: false },
    defaultVariant: on ? "on" : "off",
    // Disabled is OpenFeature's "ignore this flag entirely", which resolves to
    // the caller's default rather than to off. A surface that is off is not
    // that, so this stays false for every flag.
    disabled: false,
  };
}

/**
 * Installs the resolved flags. The values are fixed for the life of the page,
 * so this runs once during bootstrap, before React renders. Installing before
 * the first render is what keeps a surface that is off from being drawn for a
 * frame and then taken away.
 */
export function installFeatureFlags(flags: FeatureFlags = allFeaturesOn()): void {
  const configuration: Record<string, SurfaceFlag> = {};
  for (const name of featureFlagNames) {
    configuration[name] = surfaceFlag(flags[name]);
  }

  OpenFeature.setProvider(featureFlagDomain, new TypedInMemoryProvider(configuration));
}

/** FeatureFlagProvider binds the tree below it to the product's flag domain. */
export function FeatureFlagProvider({ children }: { children: ReactNode }) {
  return <OpenFeatureProvider domain={featureFlagDomain}>{children}</OpenFeatureProvider>;
}
