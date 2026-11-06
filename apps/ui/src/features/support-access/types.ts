export type SupportLevel = "readOnly" | "admin";

export type SupportState = "opening" | "open" | "closed";

/**
 * Why a window closed. The control plane may name a reason this console does
 * not know yet, so the panel reads an unknown one as a plain "ended".
 */
export type SupportClosedReason = "ended" | "expired" | "removed" | "failed";

/**
 * An environment's window. It never carries a key or how the control plane
 * reaches the environment. `shareNote` is the text to send a support engineer
 * so they can connect, with the private address in it. Only a member who may
 * change access receives it, and only on an open window.
 */
export type SupportWindow = {
  state: SupportState;
  level: SupportLevel;
  expiresAt: string;
  caseReference?: string;
  /** Where an extension the environment has not confirmed yet would move `expiresAt`. */
  extendingTo?: string;
  closedReason?: SupportClosedReason | (string & {});
  shareNote?: string;
};

export type SupportOpenInput = {
  level: SupportLevel;
  hours: number;
  caseReference: string;
};
