import type { DatabaseEngine } from "./types";

export type EngineOption = {
  id: DatabaseEngine;
  label: string;
  detail: string;
  enabled: boolean;
};

export const engines: EngineOption[] = [
  {
    id: "mysql",
    label: "MySQL",
    detail: "The usual choice for existing MySQL apps.",
    enabled: true,
  },
  {
    id: "postgresql",
    label: "PostgreSQL",
    detail: "The usual choice for existing PostgreSQL apps.",
    enabled: true,
  },
  {
    id: "mongodb",
    label: "MongoDB",
    detail: "The usual choice for existing MongoDB apps.",
    enabled: true,
  },
];

export const visibleEngines = engines.filter((engine) => engine.enabled);

// The PostgreSQL operator labels every member with the name plus 26
// characters, and Kubernetes refuses a label over 63. The adapter refuses a
// longer name too; checking it here only saves a round trip.
export const postgresqlNameLimit = 37;

export const postgresqlNameReason =
  "Every member's labels carry this name, and a longer one is refused.";

// The MongoDB provider derives the names of the replica set's objects from the
// database name and refuses one over 22 characters. The adapter refuses it
// too; checking it here only saves a round trip.
export const mongodbNameLimit = 22;

export const mongodbNameReason = "Its members' object names are built from it.";

/**
 * The longest name a create accepts for an engine, with why. MySQL has none
 * beyond the 63 every name has.
 */
export const createNameLimits: Partial<
  Record<DatabaseEngine, { limit: number; label: string; reason: string }>
> = {
  postgresql: {
    limit: postgresqlNameLimit,
    label: "PostgreSQL",
    reason: postgresqlNameReason,
  },
  mongodb: { limit: mongodbNameLimit, label: "MongoDB", reason: mongodbNameReason },
};

/**
 * MongoDB has no access from outside the Kubernetes cluster yet, so it has no
 * Access choice.
 */
export function hasAccessChoice(engine: DatabaseEngine): boolean {
  return engine !== "mongodb";
}

export const createPageDescription = `A ${joinLabels(visibleEngines, "or")} database on this Kubernetes cluster.`;

export const emptyListDescription = `Create a ${joinLabels(visibleEngines, "or")} database on this Kubernetes cluster.`;

export const engineChoicePhrase = joinLabels(visibleEngines, "or");

export const engineListPhrase = joinLabels(visibleEngines, "and");

function joinLabels(options: EngineOption[], conjunction: "and" | "or"): string {
  const labels = options.map((engine) => engine.label);

  if (labels.length <= 1) {
    return labels[0] ?? "";
  }

  if (labels.length === 2) {
    return `${labels[0]} ${conjunction} ${labels[1]}`;
  }

  return `${labels.slice(0, -1).join(", ")}, ${conjunction} ${labels[labels.length - 1]}`;
}
