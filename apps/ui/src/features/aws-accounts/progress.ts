import type { AwsAccount, AwsAccountStep } from "./types";

export type ConnectPhaseStatus = "pending" | "running" | "succeeded" | "failed";

export type ConnectPhase = { name: string; status: ConnectPhaseStatus };

export type ConnectProgress = {
  phases: ConnectPhase[];
  done: boolean;
  failed: boolean;
  /** The command went quiet before it finished. */
  stopped: boolean;
};

/** Command clocks and the control plane's differ a little, so a step just before the command was made still counts. */
const clockSlackMs = 30_000;
const quietMs = 90_000;

function reported(steps: AwsAccountStep[], from: number, names: string[]): AwsAccountStep[] {
  return steps.filter((step) => names.includes(step.step) && Date.parse(step.at) >= from);
}

function phaseOf(steps: AwsAccountStep[], finished: boolean): ConnectPhaseStatus {
  if (steps.some((step) => step.status === "failed")) {
    return "failed";
  }
  if (finished) {
    return "succeeded";
  }

  return steps.length > 0 ? "running" : "pending";
}

/** Where the connect command stands, from what it reported since it was made. */
export function connectProgress(
  account: AwsAccount | undefined,
  since: Date,
  now: Date = new Date(),
): ConnectProgress {
  if (!account) {
    return {
      phases: [
        { name: "Find the AWS account", status: "running" },
        { name: "Create the roles", status: "pending" },
        { name: "Check that PDS can use them", status: "pending" },
      ],
      done: false,
      failed: false,
      stopped: false,
    };
  }

  const from = since.getTime() - clockSlackMs;
  const steps = account.steps ?? [];
  const stack = reported(steps, from, ["template", "stack", "protect"]);
  const check = reported(steps, from, ["verify"]);
  const succeeded = (names: string[]) =>
    names.every((name) => stack.some((step) => step.step === name && step.status === "succeeded"));

  const roles = phaseOf(stack, succeeded(["stack", "protect"]));
  const verifyDone = check.some((step) => step.status === "succeeded") && account.state === "ready";
  let verify = phaseOf(check, verifyDone);
  if (verify === "pending" && roles === "succeeded") {
    verify = "running";
  }

  const done = roles === "succeeded" && verify === "succeeded";
  const failed = roles === "failed" || verify === "failed";
  const lastSignal = account.signalAt
    ? Date.parse(account.signalAt)
    : Date.parse(account.createdAt);
  const stopped = !done && !failed && now.getTime() - lastSignal > quietMs;

  return {
    phases: [
      { name: "Find the AWS account", status: "succeeded" },
      { name: "Create the roles", status: roles },
      { name: "Check that PDS can use them", status: verify },
    ],
    done,
    failed,
    stopped,
  };
}
