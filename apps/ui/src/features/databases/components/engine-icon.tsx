import { Database as DatabaseGlyph } from "lucide-react";
import { cn } from "@/lib/utils";
import type { DatabaseEngine } from "../types";

const engineLabels: Record<DatabaseEngine, string> = {
  mysql: "MySQL",
  postgresql: "PostgreSQL",
};

/** The engine as a person names it, with the version when the adapter has one. */
export function engineLabel(engine: DatabaseEngine, version?: string): string {
  const label = engineLabels[engine] ?? engine;

  return version ? `${label} ${version}` : label;
}

type EngineIconProps = {
  engine: DatabaseEngine;
  className?: string;
};

/**
 * The engine mark. It stays engine generic so a second engine is one entry in
 * the table above, not a new component.
 */
export function EngineIcon({ engine, className }: EngineIconProps) {
  return (
    <DatabaseGlyph
      className={cn("size-4 text-muted-foreground", className)}
      strokeWidth={1.5}
      aria-hidden="true"
      data-engine={engine}
    />
  );
}
