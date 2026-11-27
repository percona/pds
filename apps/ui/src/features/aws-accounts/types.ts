/** How far PDS can use an AWS account. Pending is never shown: a row appears once its account is known. */
export type AwsAccountState = "pending" | "ready" | "unreachable" | "out_of_date";

export type AwsAccountStepStatus = "started" | "succeeded" | "failed" | "heartbeat";

/** One step the connect command reported. */
export type AwsAccountStep = {
  step: string;
  status: AwsAccountStepStatus;
  at: string;
};

export type AwsAccount = {
  id: string;
  accountId: string;
  alias?: string;
  state: AwsAccountState;
  /** One safe sentence for an account that does not work. */
  reason?: string;
  allowedRegions: string[];
  stackRegion: string;
  templateVersion: number;
  latestTemplateVersion: number;
  checkedAt?: string;
  readyAt?: string;
  /** The last time the connect command said anything. */
  signalAt?: string;
  createdAt: string;
  /** The managed environments that run through the account. */
  environments: string[];
  /** What the command reported, oldest first. Only on one account's read. */
  steps?: AwsAccountStep[];
};

/** The connect command, shown once when it is made. */
export type AwsConnectCommand = {
  id: string;
  command: string;
  expiresAt: string;
  allowedRegions: string[];
};

export type AwsConnectTokenState = "waiting" | "connecting" | "ended" | "expired";

/** Where one connect command stands, and the account it is working on once it has found it. */
export type AwsConnectStatus = {
  id: string;
  state: AwsConnectTokenState;
  connectionId?: string;
  expiresAt: string;
  allowedRegions: string[];
};
