import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { fetchAiSettings, saveAiSettings } from "../api";
import type { AiSettings } from "../types";

const aiSettingsKey = ["organization", "ai"] as const;

/** What the organization lets agents and the Chat do. */
export function useAiSettings() {
  return useQuery({ queryKey: aiSettingsKey, queryFn: fetchAiSettings });
}

/** Saves the settings and shows the saved answer at once. */
export function useSaveAiSettings() {
  const queryClient = useQueryClient();

  return useMutation<AiSettings, ApiError, AiSettings>({
    mutationFn: saveAiSettings,
    onSuccess: (saved) => queryClient.setQueryData(aiSettingsKey, saved),
  });
}
