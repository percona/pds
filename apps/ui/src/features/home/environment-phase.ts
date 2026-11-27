import type { Environment } from "@/features/environments";

/** Where an environment stands, in the words Home counts it by. */
export type EnvironmentPhase =
  "connected" | "unreachable" | "joining" | "creating" | "failed" | "deleting";

/**
 * A managed environment is described by its state while PDS builds, breaks or
 * removes it, because its agent has not reported then and that silence is not
 * an outage. An environment that never reported is still joining: a row exists
 * from the moment its token is minted, long before the first heartbeat.
 */
export function environmentPhase(environment: Environment): EnvironmentPhase {
  const managed = environment.origin === "managed";
  if (managed) {
    switch (environment.state) {
      case "creating":
        return "creating";
      case "deleting":
        return "deleting";
      case "failed":
      case "lost":
        return "failed";
    }
  }
  if (environment.status === "connected") {
    return "connected";
  }
  if (environment.status === "pending" || !environment.lastSeen) {
    return managed ? "creating" : "joining";
  }

  return "unreachable";
}

/**
 * Whether a database can be created on it. An environment that reported before
 * and went quiet accepts the request and applies it when it reconnects, which is
 * what the environment page allows too.
 */
export function takesDatabases(environment: Environment): boolean {
  const phase = environmentPhase(environment);

  return phase === "connected" || phase === "unreachable";
}

/** A relative time placed mid sentence: "Last seen just now", not "Last seen Just now". */
export function inSentence(relative: string): string {
  return relative === "Just now" ? "just now" : relative;
}

export function createDatabasePath(environment: string): string {
  return `/environments/${encodeURIComponent(environment)}/new`;
}
