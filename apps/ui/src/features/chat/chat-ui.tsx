import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

/** A question another page asked Ask PDS to start a new chat with. */
export type ChatAsk = {
  question: string;
  /** The database the chat is about, recorded the way a chat started on its page is. */
  databaseId?: string;
};

type ChatUiValue = {
  open: boolean;
  setOpen: (open: boolean) => void;
  /**
   * Opens Ask PDS on a new chat with the question in the box, ready to send.
   * Nothing is sent until the person sends it.
   */
  ask: (question: string, options?: { databaseId?: string }) => void;
  /** The ask the widget has not taken up yet. */
  pendingAsk: ChatAsk | null;
  clearAsk: () => void;
};

const ChatUiContext = createContext<ChatUiValue | null>(null);

export function ChatUiProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [pendingAsk, setPendingAsk] = useState<ChatAsk | null>(null);

  const ask = useCallback((question: string, options?: { databaseId?: string }) => {
    setPendingAsk({ question, databaseId: options?.databaseId });
    setOpen(true);
  }, []);
  const clearAsk = useCallback(() => setPendingAsk(null), []);

  const value = useMemo(
    () => ({ open, setOpen, ask, pendingAsk, clearAsk }),
    [open, ask, pendingAsk, clearAsk],
  );

  return <ChatUiContext.Provider value={value}>{children}</ChatUiContext.Provider>;
}

export function useChatUi(): ChatUiValue {
  const value = useContext(ChatUiContext);
  if (!value) {
    throw new Error("useChatUi must be used within ChatUiProvider");
  }

  return value;
}
