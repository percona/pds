import { requestJson } from "@/lib/api";
import type { AiSettings } from "./types";

/** Reads what the organization lets agents and the Chat do. Every member may. */
export function fetchAiSettings(): Promise<AiSettings> {
  return requestJson<AiSettings>("/api/organization/ai");
}

/** Replaces the settings. Only an admin may. */
export function saveAiSettings(settings: AiSettings): Promise<AiSettings> {
  return requestJson<AiSettings>("/api/organization/ai", { method: "PUT", body: settings });
}
