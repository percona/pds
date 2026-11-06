import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import type { ApiError } from "@/lib/api";
import { sendChat, type ChatMessageInput } from "../api";
import type { ChatReply, LiveApproval } from "../types";

/**
 * Sends a chat turn. progress is the latest step the turn reported, such as
 * "Reading metrics", and is empty before the first step and after the turn.
 * text is the answer so far while the model writes it, and is empty after the
 * turn, when the reply takes its place. approvals are the changes the turn
 * asked about, with where each stands, until the turn ends.
 */
export function useSendChat() {
  const [progress, setProgress] = useState("");
  const [text, setText] = useState("");
  const [approvals, setApprovals] = useState<LiveApproval[]>([]);
  const mutation = useMutation<ChatReply, ApiError, ChatMessageInput>({
    mutationFn: (input) => {
      setProgress("");
      setText("");
      setApprovals([]);
      return sendChat(input, {
        onProgress: setProgress,
        onText: (delta) => setText((shown) => shown + delta),
        onApproval: (approval) =>
          setApprovals((shown) => [...shown, { approval, status: "open" as const }]),
        onApprovalEnd: (end) =>
          setApprovals((shown) =>
            shown.map((item) =>
              item.approval.id === end.id ? { ...item, status: end.outcome } : item,
            ),
          ),
      });
    },
    onSettled: () => {
      setProgress("");
      setText("");
      setApprovals([]);
    },
  });

  return { ...mutation, progress, text, approvals };
}
