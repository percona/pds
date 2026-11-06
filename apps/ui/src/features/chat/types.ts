export type ChatProvider = "anthropic" | "cursor";

/** default marks the model a new chat starts on. */
export type ChatModel = { id: string; displayName: string; default?: boolean };

export type ChatAction = {
  label: string;
  kind: string;
  id?: string;
  metric?: string;
  subject?: string;
  description?: string;
};

/** One saved turn. A user turn carries no actions. */
export type ChatMessage = {
  role: "user" | "assistant";
  content: string;
  actions: ChatAction[];
  createdAt: string;
};

/** One saved conversation as the list shows it. */
export type ChatConversationSummary = {
  id: string;
  title: string;
  provider: ChatProvider;
  model: string;
  databaseId?: string;
  updatedAt: string;
};

export type ChatConversationPage = {
  conversations: ChatConversationSummary[];
  nextCursor?: string;
};

/**
 * One conversation with every message. contextWindow is how many of the newest
 * messages the model is sent, so older ones are shown but no longer read.
 */
export type ChatThread = ChatConversationSummary & {
  contextWindow: number;
  messages: ChatMessage[];
};

export type ChatReply = { conversationId: string; text: string; actions: ChatAction[] };

/** One argument of a change, in words, as the approval card shows it. */
export type ApprovalLine = { label: string; value: string };

/** A change the model asked for, which is made only if the person approves it. */
export type ChatApproval = {
  id: string;
  tool: string;
  title: string;
  /** The database's name, when the change is about one. */
  database?: string;
  /** A change that cannot be undone, so approving it takes the database's name. */
  destructive: boolean;
  lines: ApprovalLine[];
  expiresAt: string;
};

/** How an approval ended. An open one has no outcome yet. */
export type ApprovalOutcome = "approved" | "rejected" | "expired" | "cancelled";

export type ChatApprovalEnd = { id: string; outcome: ApprovalOutcome };

/** An approval of the turn that is running, and where it stands. */
export type LiveApproval = { approval: ChatApproval; status: "open" | ApprovalOutcome };
