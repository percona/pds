import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { supportTicketsKey } from "@/features/support";
import { deleteConnection, putAssistant, putCursor, putServiceNow } from "../api";
import type { AssistantInput, ConnectionKind, CursorInput, ServiceNowInput } from "../types";
import { connectionsKey } from "./use-connections";

function invalidateModels(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: ["assistant-models"] });
}

function invalidate(queryClient: ReturnType<typeof useQueryClient>) {
  void queryClient.invalidateQueries({ queryKey: connectionsKey });
  void queryClient.invalidateQueries({ queryKey: supportTicketsKey });
}

export function useSaveServiceNow() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, ServiceNowInput>({
    mutationFn: (input) => putServiceNow(input),
    onSuccess: () => invalidate(queryClient),
  });
}

export function useSaveAssistant() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, AssistantInput>({
    mutationFn: (input) => putAssistant(input),
    onSuccess: () => {
      invalidate(queryClient);
      invalidateModels(queryClient);
    },
  });
}

export function useSaveCursor() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, CursorInput>({
    mutationFn: (input) => putCursor(input),
    onSuccess: () => {
      invalidate(queryClient);
      invalidateModels(queryClient);
    },
  });
}

export function useClearConnection() {
  const queryClient = useQueryClient();

  return useMutation<void, ApiError, ConnectionKind>({
    mutationFn: (kind) => deleteConnection(kind),
    onSuccess: (_data, kind) => {
      invalidate(queryClient);
      if (kind !== "servicenow") {
        invalidateModels(queryClient);
      }
    },
  });
}
