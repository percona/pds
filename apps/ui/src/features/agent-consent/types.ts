import type { AiAccess } from "@/features/organization-ai";

/** Who is asking, as the control plane could establish it. */
export type ConsentClient = {
  /** What the app called itself. Anybody can claim any name. */
  name: string;
  /** The address the app proved it controls. Empty for an app that registered itself and for the PDS local client. */
  host: string;
  /** "cimd" for an app with a hosted identity, "dcr" for one that registered itself, "local" for the PDS local client. */
  kind: "cimd" | "dcr" | "local";
  /** Where the answer is sent. */
  redirectHost: string;
  /** Whether the answer goes to this computer. */
  loopback: boolean;
};

export type ConsentTeam = { id: number; name: string };

/** One Organization the person may sign the app in to, and what they may give it there. */
export type ConsentOrganization = {
  id: number;
  name: string;
  role: string;
  teams: ConsentTeam[];
  ownTeamId: number;
  /** The highest tier offered. */
  access: AiAccess;
  passwords: boolean;
  sqlWrites: boolean;
};

export type ConsentRequest = {
  client: ConsentClient;
  expiresAt: string;
  organizations: ConsentOrganization[];
};

export type ConsentChoice = {
  organizationId: number;
  teamId: number;
  access: AiAccess;
  passwords: boolean;
  sqlWrites: boolean;
};
