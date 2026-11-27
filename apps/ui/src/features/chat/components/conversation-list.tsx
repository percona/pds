import { AlertCircle, MessageSquare, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { databaseNameFromId } from "../actions";
import { useConversations, useDeleteConversation } from "../hooks/use-conversations";
import type { ChatConversationSummary } from "../types";

/**
 * Saved conversations, newest first. Picking one reopens it; each row can be
 * deleted after a confirm. Disabled, no chat can be opened, so a reply in
 * flight keeps the chat it belongs to on screen.
 */
export function ConversationList({
  activeId,
  disabled,
  onOpen,
  onDeleted,
}: {
  activeId: string | undefined;
  disabled: boolean;
  onOpen: (conversation: ChatConversationSummary) => void;
  onDeleted: (id: string) => void;
}) {
  const conversations = useConversations();
  const [pendingDelete, setPendingDelete] = useState<ChatConversationSummary | undefined>();

  if (conversations.isPending) {
    return (
      <div className="space-y-2" aria-hidden="true">
        {[0, 1, 2].map((row) => (
          <div key={row} className="h-12 animate-pulse rounded-md bg-accent" />
        ))}
      </div>
    );
  }

  if (conversations.isError) {
    return (
      <div role="alert" className="space-y-3">
        <p className="flex items-start gap-2 text-sm text-destructive">
          <AlertCircle className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
          {conversations.error.message}
        </p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void conversations.refetch()}
        >
          Try again
        </Button>
      </div>
    );
  }

  const items = conversations.data.pages.flatMap((page) => page.conversations ?? []);

  if (items.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 py-8 text-center">
        <MessageSquare
          className="size-5 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        <p className="text-sm font-medium">No saved chats yet</p>
        <p className="text-sm text-muted-foreground">Send a message to start one.</p>
      </div>
    );
  }

  return (
    <>
      <ul aria-label="Saved chats" className="space-y-1">
        {items.map((conversation) => (
          <li key={conversation.id} className="flex items-center gap-1">
            <button
              type="button"
              disabled={disabled}
              onClick={() => onOpen(conversation)}
              aria-current={conversation.id === activeId ? "true" : undefined}
              className={cn(
                "min-w-0 flex-1 rounded-md px-2.5 py-2 text-left hover:bg-accent disabled:pointer-events-none disabled:opacity-50",
                "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
                conversation.id === activeId && "bg-accent",
              )}
            >
              <span className="block truncate text-sm">{conversation.title}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {conversationMeta(conversation)}
              </span>
            </button>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              aria-label={`Delete chat ${conversation.title}`}
              onClick={() => setPendingDelete(conversation)}
            >
              <Trash2 className="size-4" strokeWidth={1.5} aria-hidden="true" />
            </Button>
          </li>
        ))}
      </ul>
      {conversations.hasNextPage ? (
        <div className="flex justify-center pt-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            loading={conversations.isFetchingNextPage}
            onClick={() => void conversations.fetchNextPage()}
          >
            {conversations.isFetchingNextPage ? "Loading..." : "Load more"}
          </Button>
        </div>
      ) : null}

      <DeleteChatDialog
        conversation={pendingDelete}
        onOpenChange={(open) => {
          if (!open) {
            setPendingDelete(undefined);
          }
        }}
        onDeleted={(id) => {
          setPendingDelete(undefined);
          onDeleted(id);
        }}
      />
    </>
  );
}

function conversationMeta(conversation: ChatConversationSummary): string {
  const parts: string[] = [];
  if (conversation.databaseId) {
    parts.push(databaseNameFromId(conversation.databaseId));
  }
  const when = formatRelativeTime(conversation.updatedAt);
  if (when) {
    parts.push(when);
  }

  return parts.join(" · ");
}

function DeleteChatDialog({
  conversation,
  onOpenChange,
  onDeleted,
}: {
  conversation: ChatConversationSummary | undefined;
  onOpenChange: (open: boolean) => void;
  onDeleted: (id: string) => void;
}) {
  const remove = useDeleteConversation();

  return (
    <Dialog
      open={conversation !== undefined}
      onOpenChange={(open) => {
        if (!open) {
          remove.reset();
        }
        onOpenChange(open);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Delete this chat?</DialogTitle>
          <DialogDescription>
            The conversation and every message in it are deleted for good.
          </DialogDescription>
        </DialogHeader>
        {conversation ? <p className="truncate text-sm">{conversation.title}</p> : null}
        {remove.error ? (
          <p role="alert" className="flex items-start gap-2 text-sm text-destructive">
            <AlertCircle className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
            {remove.error.message}
          </p>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            loading={remove.isPending}
            onClick={() => {
              if (!conversation) {
                return;
              }
              remove.mutate(conversation.id, {
                onSuccess: () => {
                  toast.success("Chat deleted");
                  onDeleted(conversation.id);
                },
              });
            }}
          >
            {remove.isPending ? "Deleting..." : "Delete chat"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
