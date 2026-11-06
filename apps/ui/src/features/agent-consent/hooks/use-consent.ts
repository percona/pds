import { useMutation, useQuery } from "@tanstack/react-query";
import type { ApiError } from "@/lib/api";
import { approveConsent, denyConsent, lookupConsent } from "../api";
import type { ConsentChoice, ConsentRequest } from "../types";

/**
 * The parked request. It is read once: asking again would not change the
 * answer, and a request that is gone stays gone. A page that will not show the
 * request, because it is framed, does not read it either.
 */
export function useConsentRequest(request: string, allowed = true) {
  return useQuery<ConsentRequest, ApiError>({
    queryKey: ["agent-consent", request],
    queryFn: () => lookupConsent(request),
    enabled: allowed && request !== "",
    retry: false,
    staleTime: Infinity,
    gcTime: 0,
  });
}

/** Allows the app. The result is the address to send the browser to. */
export function useApproveConsent(request: string) {
  return useMutation<string, ApiError, ConsentChoice>({
    mutationFn: (choice) => approveConsent(request, choice),
  });
}

/** Turns the app away. The result is the address to send the browser to. */
export function useDenyConsent(request: string) {
  return useMutation<string, ApiError, void>({ mutationFn: () => denyConsent(request) });
}
