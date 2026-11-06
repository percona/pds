import { requestJson } from "@/lib/api";
import type { ConsentChoice, ConsentRequest } from "./types";

type Redirect = { redirectUrl: string };

/** Reads the parked sign in request and the choices the person has. It decides nothing. */
export function lookupConsent(request: string): Promise<ConsentRequest> {
  return requestJson<ConsentRequest>("/api/agent-consent/lookup", {
    method: "POST",
    body: { request },
  });
}

/** Allows the app with the choices made, and returns where to send the browser. */
export async function approveConsent(request: string, choice: ConsentChoice): Promise<string> {
  const answer = await requestJson<Redirect>("/api/agent-consent/approve", {
    method: "POST",
    body: { request, ...choice },
  });

  return answer.redirectUrl;
}

/** Turns the app away, and returns where to send the browser. */
export async function denyConsent(request: string): Promise<string> {
  const answer = await requestJson<Redirect>("/api/agent-consent/deny", {
    method: "POST",
    body: { request },
  });

  return answer.redirectUrl;
}
