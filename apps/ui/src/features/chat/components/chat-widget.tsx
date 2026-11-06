import { useQueryClient } from "@tanstack/react-query";
import { AlertCircle, Loader2, MessageSquare } from "lucide-react";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from "react";
import { useMatch, useNavigate } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Combobox } from "@/components/ui/combobox";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useChatUi } from "@/features/chat/chat-ui";
import { ModelKeyCards, useConnections } from "@/features/connections";
import { databaseHref, databaseKey, fetchDatabase } from "@/features/databases";
import {
  ContactSupportDialog,
  buildTicketContext,
  useSupportAvailability,
} from "@/features/support";
import type { TicketContextInput } from "@/features/support";
import { resolveChatAction } from "../actions";
import { ChatCutOffError, type ApprovalDecision } from "../api";
import {
  isFirstLine,
  isLastLine,
  noHistoryCursor,
  recallNext,
  recallPrevious,
  type HistoryCursor,
  type Recall,
} from "../history";
import {
  appendTurns,
  conversationKey,
  conversationsKey,
  isAwaitingReply,
  useConversation,
} from "../hooks/use-conversations";
import { useModels } from "../hooks/use-models";
import { useDecideApproval } from "../hooks/use-decide-approval";
import { useSendChat } from "../hooks/use-send-chat";
import type {
  ChatAction,
  ChatConversationSummary,
  ChatMessage,
  ChatProvider,
  ChatThread,
  LiveApproval,
} from "../types";
import { ApprovalCard } from "./approval-card";
import { ConversationList } from "./conversation-list";

const providerLabels: Record<ChatProvider, string> = {
  anthropic: "Anthropic",
  cursor: "Cursor",
};

/**
 * A conversation saved before a provider was removed still names it. The
 * wire carries any string, so this narrows to the providers the widget offers.
 */
function isOfferedProvider(name: string): name is ChatProvider {
  return Object.hasOwn(providerLabels, name);
}

/**
 * A random id for a first message. getRandomValues rather than randomUUID,
 * because the control plane can be served over plain HTTP, where randomUUID
 * does not exist.
 */
function mintRequestKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));

  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** How many of the newest messages the model reads, until a thread says otherwise. */
const defaultContextWindow = 32;

/**
 * The database whose page is open, so a conversation started there records it.
 * The id is the two path segments. The create form is not a database page.
 */
function usePageDatabaseId(): string | undefined {
  const match = useMatch("/databases/:environment/:id");
  if (match?.params.environment && match.params.id) {
    return `${match.params.environment}/${match.params.id}`;
  }

  return undefined;
}

export function ChatWidget() {
  const { open, setOpen } = useChatUi();
  const queryClient = useQueryClient();
  const pageDatabaseId = usePageDatabaseId();
  const onChatPage = Boolean(useMatch({ path: "/chat", end: true }));
  const navigate = useNavigate();
  const connections = useConnections();
  const support = useSupportAvailability();
  const send = useSendChat();
  const decide = useDecideApproval();
  const waitingForApproval = send.approvals.some((item) => item.status === "open");
  const openRef = useRef(open);
  openRef.current = open;

  // A provider is listed when it has a key.
  const configuredProviders = useMemo(() => {
    const items: ChatProvider[] = [];
    if (connections.data?.assistant.configured) {
      items.push("anthropic");
    }
    if (connections.data?.cursor.configured) {
      items.push("cursor");
    }
    return items;
  }, [connections.data?.assistant.configured, connections.data?.cursor.configured]);

  // provider and modelId are the choice for the next new chat. An open
  // conversation keeps the provider and model it started with.
  const [provider, setProvider] = useState<ChatProvider | undefined>(undefined);
  const [modelId, setModelId] = useState("");
  const [draft, setDraft] = useState("");
  const [activeId, setActiveId] = useState<string | undefined>(undefined);
  // The first message of a new chat, shown until its conversation exists.
  const [pendingFirst, setPendingFirst] = useState<string | null>(null);
  // The key a first message went out with, kept when it failed so sending the
  // same text again is recognized as the same message.
  const [retryKey, setRetryKey] = useState<{ message: string; key: string } | null>(null);
  const conversation = useConversation(activeId, { poll: !send.isPending });
  const thread = activeId ? conversation.data : undefined;
  // A saved chat on a removed provider is read only: it has no provider to
  // list models for or to send to.
  const providerRemoved = thread !== undefined && !isOfferedProvider(thread.provider);
  const chatProvider = providerRemoved ? undefined : (thread?.provider ?? provider);
  const chatModel = thread?.model ?? modelId;
  const [supportOpen, setSupportOpen] = useState(false);
  const [supportSubject, setSupportSubject] = useState("");
  const [supportDescription, setSupportDescription] = useState("");
  const [supportContext, setSupportContext] = useState<TicketContextInput | undefined>(undefined);
  const threadEndRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    threadEndRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [
    thread?.messages.length,
    pendingFirst,
    send.isPending,
    send.text,
    send.approvals.length,
    open,
    onChatPage,
  ]);

  useEffect(() => {
    if (!open && !onChatPage) {
      return;
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      setOpen(false);
      if (onChatPage) {
        navigate("/");
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, onChatPage, setOpen, navigate]);

  useEffect(() => {
    if (configuredProviders.length === 0) {
      setProvider(undefined);
      return;
    }

    if (provider === undefined || !configuredProviders.includes(provider)) {
      setProvider(configuredProviders[0]);
    }
  }, [configuredProviders, provider]);

  const models = useModels(chatProvider);
  const newChatModels = useModels(provider);

  // Only the next new chat follows the model list. An open conversation keeps
  // its model even when the provider stops listing it.
  useEffect(() => {
    if (!newChatModels.data || newChatModels.data.length === 0) {
      setModelId("");
      return;
    }

    if (!newChatModels.data.some((model) => model.id === modelId)) {
      const preferred = newChatModels.data.find((model) => model.default) ?? newChatModels.data[0]!;
      setModelId(preferred.id);
    }
  }, [newChatModels.data, modelId]);

  // A new chat drops the kept retry key, so its first message is never taken
  // for a retry of the one that failed in the chat before it.
  const startNewChat = () => {
    setActiveId(undefined);
    setPendingFirst(null);
    setRetryKey(null);
  };

  const hideChat = () => {
    setOpen(false);
    if (onChatPage) {
      navigate("/");
    }
  };

  // Switching the provider or the model starts a new conversation, because an
  // existing one is answered by the model it started with.
  const handleProviderChange = (next: ChatProvider) => {
    setProvider(next);
    setModelId("");
    startNewChat();
  };

  const handleModelChange = (next: string) => {
    if (thread && isOfferedProvider(thread.provider)) {
      setProvider(thread.provider);
    }
    setModelId(next);
    startNewChat();
  };

  const handleOpenConversation = (summary: ChatConversationSummary) => {
    setActiveId(summary.id);
    if (isOfferedProvider(summary.provider)) {
      setProvider(summary.provider);
    }
    setModelId(summary.model);
    setPendingFirst(null);
  };

  const handleSend = (text?: string) => {
    const content = (text ?? draft).trim();
    if (!chatProvider || chatModel.trim() === "" || content === "") {
      return;
    }
    if (activeId && !thread) {
      return;
    }

    const continuing = activeId;
    const selection = { provider: chatProvider, model: chatModel };
    const startedOn = continuing ? undefined : pageDatabaseId;
    const requestKey = continuing
      ? undefined
      : retryKey?.message === content
        ? retryKey.key
        : mintRequestKey();
    const asked: ChatMessage = {
      role: "user",
      content,
      actions: [],
      createdAt: new Date().toISOString(),
    };
    setDraft("");
    if (continuing) {
      appendTurns(queryClient, continuing, [asked]);
    } else {
      setPendingFirst(content);
    }

    send.mutate(
      {
        ...selection,
        conversationId: continuing,
        databaseId: startedOn,
        requestKey,
        message: content,
      },
      {
        onSuccess: (reply) => {
          const id = reply.conversationId;
          const answered: ChatMessage = {
            role: "assistant",
            content: reply.text,
            actions: reply.actions ?? [],
            createdAt: new Date().toISOString(),
          };
          if (continuing) {
            appendTurns(queryClient, id, [answered]);
          } else {
            appendTurns(queryClient, id, [asked, answered], {
              id,
              title: content,
              ...selection,
              databaseId: startedOn,
              updatedAt: answered.createdAt,
              contextWindow: defaultContextWindow,
            });
            setActiveId(id);
          }
          setPendingFirst(null);
          setRetryKey(null);
          void queryClient.invalidateQueries({ queryKey: conversationsKey });
          void queryClient.invalidateQueries({ queryKey: conversationKey(id) });
          if (!openRef.current) {
            toast.success("Reply is ready");
          }
        },
        onError: (error) => {
          // The stream dropped after the message was saved, so the turn is
          // still running on the control plane. Open the chat that holds it
          // and let the reply arrive there, rather than asking twice.
          if (error instanceof ChatCutOffError && error.conversationId) {
            const saved = error.conversationId;
            setPendingFirst(null);
            setRetryKey(null);
            if (!continuing) {
              setActiveId(saved);
            }
            void queryClient.invalidateQueries({ queryKey: conversationsKey });
            void queryClient.invalidateQueries({ queryKey: conversationKey(saved) });
            toast.error(error.message);
            return;
          }

          // A turn the model did not answer is taken back out of the saved
          // thread, so the message goes back in the box to send again. It may
          // also have been saved on a connection that dropped before saying
          // so, which the kept key lets the control plane recognize.
          setPendingFirst(null);
          if (requestKey) {
            setRetryKey({ message: content, key: requestKey });
          }
          setDraft(content);
          if (continuing) {
            void queryClient.invalidateQueries({ queryKey: conversationKey(continuing) });
          }
          void queryClient.invalidateQueries({ queryKey: conversationsKey });
          if (error.status === 503) {
            toast.error(error.message, {
              action: { label: "Try again", onClick: () => handleSend(content) },
            });
          } else {
            toast.error(error.message);
          }
        },
      },
    );
  };

  // The ticket carries the database's engine and facts the way the database
  // page's does. When the read fails the name alone still goes, so the dialog
  // opens either way.
  const supportContextFor = async (
    databaseId: string,
    databaseName: string,
  ): Promise<TicketContextInput> => {
    try {
      const database = await queryClient.fetchQuery({
        queryKey: databaseKey(databaseId),
        queryFn: ({ signal }) => fetchDatabase(databaseId, signal),
        staleTime: 30_000,
      });

      return buildTicketContext(database, { errorMessage: database.message });
    } catch {
      return { database: databaseName };
    }
  };

  const handleAction = async (action: ChatAction, assistantText: string) => {
    const resolved = resolveChatAction(action, assistantText);
    if (!resolved) {
      return;
    }

    if (resolved.type === "support") {
      setSupportSubject(resolved.intent.subject ?? "");
      setSupportDescription(resolved.intent.description ?? "");
      setSupportContext(
        resolved.databaseId && resolved.databaseName
          ? await supportContextFor(resolved.databaseId, resolved.databaseName)
          : undefined,
      );
      setSupportOpen(true);
      return;
    }

    setOpen(false);
    navigate(databaseHref(resolved.databaseId), {
      state: { chatIntent: resolved.intent },
    });
  };

  const conversationList = (
    <ConversationList
      activeId={activeId}
      onOpen={handleOpenConversation}
      onDeleted={(id) => {
        if (id === activeId) {
          startNewChat();
        }
      }}
    />
  );

  // Show chats leaves the floating panel for a screen that is only chats:
  // the saved list on the left, the open conversation on the right.
  if (onChatPage) {
    return (
      <>
        <section
          id="environment-chat"
          role="region"
          aria-label="Chat"
          className="fixed inset-y-0 right-0 left-64 z-30 flex bg-background"
        >
          <aside className="flex w-80 shrink-0 flex-col border-r border-border bg-card">
            <header className="flex h-14 items-center px-4">
              <h1 className="text-2xl font-semibold tracking-tight">Chats</h1>
            </header>
            <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-4">{conversationList}</div>
          </aside>
          <div className="flex min-w-0 flex-1 flex-col">
            <header className="flex h-14 items-center justify-end gap-1 border-b border-border px-4">
              {configuredProviders.length > 0 ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="whitespace-nowrap"
                  disabled={send.isPending}
                  onClick={startNewChat}
                >
                  New chat
                </Button>
              ) : null}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="whitespace-nowrap"
                onClick={hideChat}
              >
                Hide chat
              </Button>
            </header>
            <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden p-4">
              {connections.isPending ? (
                <div className="h-24 animate-pulse rounded-lg bg-accent" />
              ) : configuredProviders.length === 0 ? (
                <div className="min-h-0 flex-1 space-y-3 overflow-y-auto">
                  <p className="text-sm text-muted-foreground">
                    Paste a Claude or Cursor key to start chatting.
                  </p>
                  {connections.data ? <ModelKeyCards data={connections.data} /> : null}
                </div>
              ) : (
                <ChatComposer
                  configuredProviders={configuredProviders}
                  provider={chatProvider}
                  providerRemoved={providerRemoved}
                  modelId={chatModel}
                  draft={draft}
                  activeId={activeId}
                  conversation={conversation}
                  pendingFirst={pendingFirst}
                  sendPending={send.isPending}
                  sendProgress={send.progress}
                  sendText={send.text}
                  approvals={send.approvals}
                  onDecide={(id, decision) => decide.mutateAsync({ id, ...decision })}
                  models={models}
                  threadEndRef={threadEndRef}
                  onProviderChange={handleProviderChange}
                  onModelChange={handleModelChange}
                  onDraftChange={setDraft}
                  onSend={() => handleSend()}
                  onAction={handleAction}
                  onNewChat={startNewChat}
                />
              )}
            </div>
          </div>
        </section>
        <ContactSupportDialog
          open={supportOpen}
          onOpenChange={setSupportOpen}
          subject={supportSubject}
          description={supportDescription}
          context={supportContext}
          connected={support.connected}
          portalUrl={support.portalUrl}
        />
      </>
    );
  }

  return (
    <>
      <div className="fixed right-6 bottom-6 z-40 flex flex-col items-end gap-2">
        {open ? (
          <section
            id="environment-chat"
            role="region"
            aria-label="Chat"
            className="flex h-[32rem] w-[24rem] max-h-[calc(100vh-5rem)] max-w-[calc(100vw-3rem)] flex-col rounded-lg border border-border bg-card shadow-lg"
          >
            <header className="flex items-center justify-between gap-2 border-b border-border px-4 py-3">
              <h2 className="text-base font-semibold">Chat</h2>
              <div className="flex items-center gap-1">
                {configuredProviders.length > 0 ? (
                  <>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="whitespace-nowrap"
                      disabled={send.isPending}
                      onClick={() => navigate("/chat")}
                    >
                      Show chats
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="whitespace-nowrap"
                      disabled={send.isPending}
                      onClick={startNewChat}
                    >
                      New chat
                    </Button>
                  </>
                ) : null}
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="whitespace-nowrap"
                  onClick={hideChat}
                >
                  Hide chat
                </Button>
              </div>
            </header>
            <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-hidden p-4">
              {connections.isPending ? (
                <div className="h-24 animate-pulse rounded-lg bg-accent" />
              ) : configuredProviders.length === 0 ? (
                <div className="min-h-0 flex-1 space-y-3 overflow-y-auto">
                  <p className="text-sm text-muted-foreground">
                    Paste a Claude or Cursor key to start chatting.
                  </p>
                  {connections.data ? <ModelKeyCards data={connections.data} /> : null}
                </div>
              ) : (
                <ChatComposer
                  configuredProviders={configuredProviders}
                  provider={chatProvider}
                  providerRemoved={providerRemoved}
                  modelId={chatModel}
                  draft={draft}
                  activeId={activeId}
                  conversation={conversation}
                  pendingFirst={pendingFirst}
                  sendPending={send.isPending}
                  sendProgress={send.progress}
                  sendText={send.text}
                  approvals={send.approvals}
                  onDecide={(id, decision) => decide.mutateAsync({ id, ...decision })}
                  models={models}
                  threadEndRef={threadEndRef}
                  onProviderChange={handleProviderChange}
                  onModelChange={handleModelChange}
                  onDraftChange={setDraft}
                  onSend={() => handleSend()}
                  onAction={handleAction}
                  onNewChat={startNewChat}
                />
              )}
            </div>
          </section>
        ) : (
          <Button
            type="button"
            aria-expanded={false}
            aria-controls="environment-chat"
            onClick={() => setOpen(true)}
          >
            {send.isPending ? (
              <>
                <Loader2 className="animate-spin" strokeWidth={1.5} aria-hidden="true" />
                {waitingForApproval ? "Waiting for your approval" : "Working on a reply"}
              </>
            ) : (
              <>
                <MessageSquare strokeWidth={1.5} aria-hidden="true" />
                Open chat
              </>
            )}
          </Button>
        )}
      </div>

      <ContactSupportDialog
        open={supportOpen}
        onOpenChange={setSupportOpen}
        subject={supportSubject}
        description={supportDescription}
        context={supportContext}
        connected={support.connected}
        portalUrl={support.portalUrl}
      />
    </>
  );
}

/**
 * The buttons of a reply that can be shown. A button only opens something, so
 * a scale, backup or schedule button an older reply saved is not shown.
 */
function opening(message: ChatMessage): ChatAction[] {
  return message.actions.filter((action) => resolveChatAction(action, message.content) !== null);
}

/**
 * The first message the model will read on the next turn. The next message
 * takes one of the window's places, and the window opens on a user turn, so
 * messages before this index are shown but no longer sent.
 */
function nextContextStart(messages: ChatMessage[], contextWindow: number): number {
  let start = Math.max(0, messages.length - (contextWindow - 1));
  if (start === 0) {
    return 0;
  }
  while (start < messages.length && messages[start]!.role !== "user") {
    start += 1;
  }

  return start;
}

type ModelsQuery = ReturnType<typeof useModels>;
type ConversationQuery = ReturnType<typeof useConversation>;

function ChatComposer({
  configuredProviders,
  provider,
  providerRemoved,
  modelId,
  draft,
  activeId,
  conversation,
  pendingFirst,
  sendPending,
  sendProgress,
  sendText,
  approvals,
  onDecide,
  models,
  threadEndRef,
  onProviderChange,
  onModelChange,
  onDraftChange,
  onSend,
  onAction,
  onNewChat,
}: {
  configuredProviders: ChatProvider[];
  provider: ChatProvider | undefined;
  providerRemoved: boolean;
  modelId: string;
  draft: string;
  activeId: string | undefined;
  conversation: ConversationQuery;
  pendingFirst: string | null;
  sendPending: boolean;
  sendProgress: string;
  sendText: string;
  approvals: LiveApproval[];
  onDecide: (id: string, decision: ApprovalDecision) => Promise<unknown>;
  models: ModelsQuery;
  threadEndRef: RefObject<HTMLDivElement | null>;
  onProviderChange: (next: ChatProvider) => void;
  onModelChange: (id: string) => void;
  onDraftChange: (value: string) => void;
  onSend: () => void;
  onAction: (action: ChatAction, assistantText: string) => void;
  onNewChat: () => void;
}) {
  const thread: ChatThread | undefined = activeId ? conversation.data : undefined;
  const messages = thread?.messages ?? [];
  const threadLoading = activeId !== undefined && conversation.isPending;
  const messageRef = useRef<HTMLTextAreaElement>(null);
  const [cursor, setCursor] = useState<HistoryCursor>(noHistoryCursor);

  // An open conversation's model is fixed, so a model list that failed to
  // load only blocks starting a new chat, not continuing this one.
  const canSend =
    provider !== undefined &&
    modelId.trim() !== "" &&
    draft.trim() !== "" &&
    (activeId === undefined ? !models.isError : thread !== undefined);

  // An open conversation's model stays selectable even when the provider no
  // longer lists it, so the box never reads as empty.
  const modelOptions = useMemo(() => {
    const options = (models.data ?? []).map((model) => ({
      value: model.id,
      label: model.displayName,
    }));
    if (modelId && !options.some((option) => option.value === modelId)) {
      options.unshift({ value: modelId, label: modelId });
    }
    return options;
  }, [models.data, modelId]);

  const providerOptions =
    provider && !configuredProviders.includes(provider)
      ? [provider, ...configuredProviders]
      : configuredProviders;

  // Every message the developer has already sent in this conversation, oldest first.
  const sent = useMemo(
    () => messages.flatMap((message) => (message.role === "user" ? [message.content] : [])),
    [messages],
  );

  const contextStart = nextContextStart(messages, thread?.contextWindow ?? defaultContextWindow);

  // A send (or a rolled back send) puts the box back on the live draft.
  useEffect(() => {
    setCursor(noHistoryCursor);
  }, [sent.length]);

  const applyRecall = (recall: Recall | undefined): boolean => {
    if (!recall) {
      return false;
    }

    setCursor(recall.cursor);
    onDraftChange(recall.draft);
    // A shell leaves the caret at the end of the recalled line, so this does too.
    requestAnimationFrame(() => {
      messageRef.current?.setSelectionRange(recall.draft.length, recall.draft.length);
    });

    return true;
  };

  const onMessageKeyDown = (event: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      onSend();
      return;
    }

    if (event.key === "ArrowUp" && !event.shiftKey && !event.altKey && !event.metaKey) {
      const caret = event.currentTarget.selectionStart ?? 0;
      if (isFirstLine(draft, caret) && applyRecall(recallPrevious(sent, cursor, draft))) {
        event.preventDefault();
      }
      return;
    }

    if (event.key === "ArrowDown" && !event.shiftKey && !event.altKey && !event.metaKey) {
      const caret = event.currentTarget.selectionStart ?? 0;
      if (isLastLine(draft, caret) && applyRecall(recallNext(sent, cursor))) {
        event.preventDefault();
      }
    }
  };

  return (
    <>
      <div className="min-h-0 flex-1 space-y-3 overflow-y-auto">
        {threadLoading ? (
          <div className="space-y-3" aria-hidden="true">
            <div className="ml-auto h-9 w-2/3 animate-pulse rounded-lg bg-accent" />
            <div className="h-16 w-5/6 animate-pulse rounded-lg bg-accent" />
          </div>
        ) : null}
        {activeId !== undefined && conversation.isError && !conversation.data ? (
          <div role="alert" className="space-y-3">
            <p className="flex items-start gap-2 text-sm text-destructive">
              <AlertCircle
                className="mt-0.5 size-4 shrink-0"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              {conversation.error.message}
            </p>
            <div className="flex gap-2">
              {conversation.error.status === 404 ? null : (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => void conversation.refetch()}
                >
                  Try again
                </Button>
              )}
              <Button type="button" size="sm" variant="outline" onClick={onNewChat}>
                New chat
              </Button>
            </div>
          </div>
        ) : null}
        {messages.map((message, index) => (
          <div key={`${message.role}-${index}`} className="space-y-3">
            {index === contextStart && contextStart > 0 ? (
              <p
                role="note"
                className="border-t border-border pt-2 text-center text-xs text-muted-foreground"
              >
                Messages above this line are no longer sent to the model.
              </p>
            ) : null}
            {message.role === "user" ? (
              <div className="flex justify-end">
                <div className="max-w-[75%] rounded-lg bg-primary px-3 py-2 text-sm whitespace-pre-wrap text-primary-foreground">
                  {message.content}
                </div>
              </div>
            ) : (
              <div className="space-y-2">
                <div className="max-w-[75%] text-sm whitespace-pre-wrap">{message.content}</div>
                {opening(message).length > 0 ? (
                  <div className="flex flex-wrap gap-2">
                    {opening(message).map((action, actionIndex) => (
                      <Button
                        key={`${action.label}-${actionIndex}`}
                        type="button"
                        size="sm"
                        variant="outline"
                        onClick={() => void onAction(action, message.content)}
                      >
                        {action.label}
                      </Button>
                    ))}
                  </div>
                ) : null}
              </div>
            )}
          </div>
        ))}
        {messages.length === 0 && pendingFirst === null && !threadLoading ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 py-16 text-center">
            <p className="text-sm font-medium">Start a chat</p>
            <p className="max-w-sm text-sm text-muted-foreground">
              Ask about a database, a backup, or what to do next.
            </p>
          </div>
        ) : null}
        {pendingFirst !== null ? (
          <div className="flex justify-end">
            <div className="max-w-[75%] rounded-lg bg-primary px-3 py-2 text-sm whitespace-pre-wrap text-primary-foreground">
              {pendingFirst}
            </div>
          </div>
        ) : null}
        {!sendPending && isAwaitingReply(thread) ? (
          <div
            role="status"
            className="flex max-w-[85%] items-center gap-2 rounded-lg border border-border bg-background px-3 py-2 text-sm text-muted-foreground"
          >
            <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
            Waiting for the reply
          </div>
        ) : null}
        {sendPending && sendText !== "" ? (
          <div className="max-w-[75%] text-sm whitespace-pre-wrap">{sendText}</div>
        ) : null}
        {sendPending
          ? approvals.map(({ approval, status }) => (
              <ApprovalCard
                key={approval.id}
                approval={approval}
                status={status}
                onDecide={(decision) => onDecide(approval.id, decision)}
              />
            ))
          : null}
        {sendPending ? (
          <div
            role="status"
            className="flex max-w-[85%] items-center gap-2 rounded-lg border border-border bg-background px-3 py-2 text-sm text-muted-foreground"
          >
            <Loader2 className="size-4 animate-spin" strokeWidth={1.5} aria-hidden="true" />
            {approvals.some((item) => item.status === "open")
              ? "Waiting for your approval"
              : sendProgress || "Working on a reply"}
          </div>
        ) : null}
        <div ref={threadEndRef} />
      </div>

      <div className="space-y-2 rounded-lg border border-input bg-background p-2">
        <div className="flex flex-wrap items-center gap-2">
          <div className="min-w-36 flex-1">
            <Label htmlFor="chat-provider" className="sr-only">
              Provider
            </Label>
            <Select
              value={provider}
              onValueChange={(value) => onProviderChange(value as ChatProvider)}
              disabled={sendPending}
            >
              <SelectTrigger id="chat-provider" aria-label="Provider">
                <SelectValue placeholder="Choose a provider" />
              </SelectTrigger>
              <SelectContent>
                {providerOptions.map((item) => (
                  <SelectItem key={item} value={item}>
                    {providerLabels[item]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="min-w-36 flex-1">
            <Label htmlFor="chat-model" className="sr-only">
              Model
            </Label>
            <Combobox
              id="chat-model"
              ariaLabel="Model"
              options={modelOptions}
              value={modelId}
              onChange={onModelChange}
              disabled={models.isPending || models.isError || sendPending}
              placeholder={models.isPending ? "Loading models..." : "Choose a model"}
              searchLabel="Search models"
              searchPlaceholder="Search models"
              emptyText="No models match that search."
            />
          </div>
        </div>
        {models.isError ? <p className="text-sm text-destructive">{models.error.message}</p> : null}
        {providerRemoved ? (
          <p className="text-xs text-muted-foreground">
            This chat used a provider that is no longer offered. Start a new chat.
          </p>
        ) : activeId !== undefined ? (
          <p className="text-xs text-muted-foreground">
            Changing the provider or model starts a new chat.
          </p>
        ) : null}

        <div className="flex items-end gap-2">
          <Label htmlFor="chat-message" className="sr-only">
            Message
          </Label>
          <textarea
            ref={messageRef}
            id="chat-message"
            rows={2}
            value={draft}
            placeholder="Ask a question"
            onChange={(event) => onDraftChange(event.target.value)}
            onKeyDown={onMessageKeyDown}
            disabled={sendPending}
            className="max-h-32 min-h-10 flex-1 resize-none bg-transparent px-2 py-2 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none"
          />
          <Button type="button" loading={sendPending} disabled={!canSend} onClick={onSend}>
            {sendPending ? "Sending..." : "Send"}
          </Button>
        </div>
        {sent.length > 0 ? (
          <p className="px-2 text-xs text-muted-foreground">
            Press the up arrow to load a message you already sent.
          </p>
        ) : null}

        {sendPending ? (
          <p className="text-xs text-muted-foreground">This can take a minute.</p>
        ) : null}
      </div>
    </>
  );
}
