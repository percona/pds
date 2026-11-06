import { ApiError, requestJson } from "@/lib/api";
import { allFeaturesOn, isFeatureFlagName, type FeatureFlags } from "./types";

type FeaturesResponse = { flags?: unknown };

/**
 * How long the bootstrap waits for the flag set. This read sits ahead of the
 * first render, so an adapter that accepts the connection and then stalls
 * would otherwise hold the page blank for as long as the browser allows.
 */
const flagReadTimeoutMs = 3000;

/**
 * Reads the flag set the adapter publishes. The UI is a static bundle served
 * from the adapter's own origin, so this route is how a chart value reaches
 * the browser.
 *
 * Only an adapter with no flag route, one built before flags existed, reads as
 * every surface on. Any other failure rejects. A failed read says nothing about
 * what the deployment turned off, so answering "everything on" would draw a
 * surface the operator removed and keep drawing it for the life of the page.
 * The caller retries and then reports the failure.
 */
export async function fetchFeatureFlags(): Promise<FeatureFlags> {
  try {
    const payload = await requestJson<FeaturesResponse>("/api/features", {
      signal: AbortSignal.timeout(flagReadTimeoutMs),
    });

    return readFeatureFlags(payload?.flags);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      return allFeaturesOn();
    }

    throw error;
  }
}

/**
 * Reads the adapter's flag map, keeping only booleans under names this build
 * knows. An unknown name is dropped and a missing one stays on, so the two
 * halves can be deployed in either order.
 */
export function readFeatureFlags(raw: unknown): FeatureFlags {
  const flags = allFeaturesOn();
  if (typeof raw !== "object" || raw === null) {
    return flags;
  }

  for (const [name, value] of Object.entries(raw)) {
    if (isFeatureFlagName(name) && typeof value === "boolean") {
      flags[name] = value;
    }
  }

  return flags;
}
