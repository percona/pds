import type { ReactNode } from "react";
import { useFeature } from "../hooks/use-feature";
import type { FeatureFlagName } from "../types";

type FeatureProps = {
  name: FeatureFlagName;
  children: ReactNode;
  /** What to render instead when the surface is off. Nothing, by default. */
  fallback?: ReactNode;
};

/** Feature renders its children only while the named surface is on. */
export function Feature({ name, children, fallback = null }: FeatureProps) {
  return <>{useFeature(name) ? children : fallback}</>;
}
