import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { deleteConversation, fetchConversation, fetchConversations } from "../api";
import type { ChatConversationPage, ChatMessage, ChatThread } from "../types";

export const conversationsKey = ["chat-conversations"] as const;

export function conversationKey(id: string) {
  return ["chat-conversation", id] as const;
}

/** Saved conversations, newest first, one page at a time. */
export function useConversations(enabled = true) {
  return useInfiniteQuery<
    ChatConversationPage,
    ApiError,
    { pages: ChatConversationPage[] },
    typeof conversationsKey,
    string | undefined
  >({
    queryKey: conversationsKey,
    queryFn: ({ pageParam, signal }) => fetchConversations(pageParam, signal),
    initialPageParam: undefined,
    getNextPageParam: (last) => last.nextCursor || undefined,
    enabled,
  });
}

/** How often an open chat checks for a reply that is still being answered. */
const pendingReplyPoll = 3_000;

/**
 * How long a question can wait for its reply. A turn runs for at most a few
 * minutes, so a question older than this is no longer being answered.
 */
const pendingReplyWindow = 3 * 60_000;

/** True while the thread's newest message is a question still being answered. */
export function isAwaitingReply(thread: ChatThread | undefined, now: number = Date.now()): boolean {
  const last = thread?.messages.at(-1);

  return last?.role === "user" && now - Date.parse(last.createdAt) < pendingReplyWindow;
}

/**
 * One conversation and every message in it. Undefined is a new chat.
 *
 * While the newest message is a recent question and this browser is not the
 * one waiting on it, the thread is read again every few seconds. That is how
 * a reply whose stream dropped, or one asked from another tab, shows up.
 */
export function useConversation(id: string | undefined, { poll = true }: { poll?: boolean } = {}) {
  return useQuery<ChatThread, ApiError>({
    queryKey: id ? conversationKey(id) : ["chat-conversation", "none"],
    queryFn: ({ signal }) => fetchConversation(id!, signal),
    enabled: id !== undefined,
    refetchInterval: (query) =>
      poll && isAwaitingReply(query.state.data) ? pendingReplyPoll : false,
  });
}

export function useDeleteConversation() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, string>({
    mutationFn: deleteConversation,
    onSuccess: (_, id) => {
      queryClient.removeQueries({ queryKey: conversationKey(id) });
      void queryClient.invalidateQueries({ queryKey: conversationsKey });
    },
  });
}

/**
 * Puts turns at the end of a cached conversation, so a sent message and its
 * reply show at once rather than after a refetch. A conversation the cache has
 * not seen yet starts from seed.
 */
export function appendTurns(
  queryClient: QueryClient,
  id: string,
  turns: ChatMessage[],
  seed?: Omit<ChatThread, "messages">,
) {
  queryClient.setQueryData<ChatThread>(conversationKey(id), (current) => {
    const base = current ?? (seed ? { ...seed, messages: [] } : undefined);
    if (!base) {
      return current;
    }

    return { ...base, messages: [...base.messages, ...turns] };
  });
}
