import { useBooleanFlagValue } from "@openfeature/react-sdk";
import type { FeatureFlagName } from "../types";

/**
 * Reports whether a product surface is on. The default is on, so a name this
 * adapter has not heard of never hides the screen behind it.
 */
export function useFeature(name: FeatureFlagName): boolean {
  return useBooleanFlagValue(name, true);
}
