/** The actions the adapter records. Anything else renders as its raw value. */
export type ActivityAction =
  | "database.create"
  | "database.scale"
  | "database.backup"
  | "database.backup-schedule"
  | "database.restore"
  | "database.delete"
  | "connection.update"
  | "connection.delete"
  | "monitoring.password-read"
  | "database.credential-read"
  | "agent.revoke";

/** The client kinds the adapter records. Anything else renders as its raw value. */
export type ActivityClient = "mcp" | "chat";

/** Whether the mutation reached the platform. */
export type ActivityOutcome = "succeeded" | "accepted" | "failed";

/** One recorded control plane mutation. */
export type ActivityEntry = {
  /** Zero padded sequence. It sorts lexically and doubles as the cursor. */
  id: string;
  at: string;
  actor: string;
  /**
   * How the actor's session signed in, such as "mcp". Absent for the UI, for
   * curl, and on entries recorded before the adapter tracked it.
   */
  client?: ActivityClient | string;
  /**
   * The program behind the client: the agent's name for an MCP client, such as
   * "Cursor", or the provider for the Chat, such as "Anthropic". Absent for a
   * person in the console.
   */
  agent?: string;
  requestId?: string;
  /** The Team the change was made in. Present when the feed covers every Team. */
  team?: string;
  action: ActivityAction | string;
  databaseId?: string;
  target: string;
  outcome: ActivityOutcome | string;
  detail?: string;
  error?: string;

  /**
   * The object the action produced. Absent on rows recorded before the detail
   * view existed, and on every connection action, which touches no cluster
   * object.
   */
  resourceKind?: ActivityResourceKind | string;
  resourceName?: string;
};

/** The kinds of object an action can produce. */
export type ActivityResourceKind = "instance" | "backup" | "restore";

/** Where one timeline line came from. They fail in different ways, so a
 * reader has to be able to tell them apart. */
export type TimelineSource = "request" | "resource" | "cluster";

/** Timeline line types, matching the Kubernetes event types. */
export type TimelineType = "normal" | "warning";

/** The live state of the object one action produced. */
export type ActivityResource = {
  kind: ActivityResourceKind | string;
  name: string;
  namespace?: string;
  state: string;
  message?: string;
  startedAt?: string;
  endedAt?: string;
  sizeBytes?: number;
};

/** One line of what happened, in the order it happened. */
export type TimelineEntry = {
  at: string;
  source: TimelineSource | string;
  type: TimelineType | string;
  reason?: string;
  message?: string;
  /** Above one when Kubernetes folded a repeat into one record. */
  count?: number;
};

/**
 * One answer to GET /api/activity/:id.
 *
 * The event is what was recorded when the request returned. Resource and
 * timeline are read live, so a detail can contradict the row it was opened
 * from: the request succeeded and the operation it started did not.
 */
export type ActivityDetail = {
  event: ActivityEntry;
  resource?: ActivityResource;
  timeline: TimelineEntry[];
  /** A source that could not be read, stated rather than left as a gap. */
  notes?: string[];
  truncated: boolean;
};

/** One answer to GET /api/activity. */
export type ActivityPage = {
  entries: ActivityEntry[];
  /**
   * The newest id in the whole ring, not in this page. A poll hands it back
   * as `since` and gets only what arrived after it.
   */
  cursor: string;
  truncated: boolean;
};
