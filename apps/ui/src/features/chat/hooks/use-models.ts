import { useQuery } from "@tanstack/react-query";
import { useFeature } from "@/features/flags";
import type { ApiError } from "@/lib/api";
import { fetchModels } from "../api";
import type { ChatModel, ChatProvider } from "../types";

export function modelsKey(provider: ChatProvider) {
  return ["assistant-models", provider] as const;
}

export function useModels(provider: ChatProvider | undefined) {
  // The route behind this belongs to the assistant surface, so with that off
  // the list stays empty rather than asking for a 501.
  const assistantOn = useFeature("assistant");

  return useQuery<ChatModel[], ApiError>({
    queryKey: provider ? modelsKey(provider) : ["assistant-models", "none"],
    queryFn: ({ signal }) => fetchModels(provider!, signal),
    enabled: assistantOn && provider !== undefined,
  });
}
