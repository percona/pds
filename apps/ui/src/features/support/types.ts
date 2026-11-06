/**
 * Support ticket types. Almost every field is optional: the adapter leaves out
 * what ServiceNow did not report rather than sending an empty string, and a
 * create answers with less than a details read.
 */
export type SupportTicket = {
  /** The number a developer quotes, such as `CS0061234`. */
  number?: string;
  /** The ServiceNow record id. */
  id?: string;
  state?: string;
  priority?: string;
  account?: string;
  contact?: string;
  subject?: string;
  description?: string;
  technology?: string;
  openedAt?: string;
  resolvedAt?: string;
  updatedAt?: string;
  comments?: TicketComment[];
  /** Where to read the ticket in the Percona Support Portal. */
  portalUrl?: string;
  /** A stable ServiceNow link for this specific ticket when available. */
  externalUrl?: string;
};

export type TicketComment = {
  at?: string;
  author?: string;
  /** Journal label, such as `Additional comments`, when ServiceNow provides one. */
  label?: string;
  body: string;
};

/** The account's tickets, plus the portal this environment is connected to. */
export type TicketList = {
  tickets: SupportTicket[];
  portalUrl?: string;
};

/**
 * The database facts a ticket carries. The adapter assembles them into the
 * description and sanitizes the result, so nothing here is formatted here.
 */
export type TicketContextInput = {
  database?: string;
  profile?: string;
  engine?: string;
  engineVersion?: string;
  clusterType?: string;
  namespace?: string;
  members?: number;
  status?: string;
  errorMessage?: string;
};

export type CreateTicketInput = {
  subject: string;
  description: string;
  context?: TicketContextInput;
};

export type ReplyTicketInput = {
  body: string;
};
