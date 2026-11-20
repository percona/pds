import { useQuery } from "@tanstack/react-query";
import { fetchAwsAccount, fetchAwsAccounts } from "./api";

export const awsAccountsKey = ["aws-accounts", "list"] as const;

export const awsAccountsPollMs = 15_000;

/** The AWS accounts the acting Team connected. `pollMs` is faster while a command runs. */
export function useAwsAccounts(options: { pollMs?: number; enabled?: boolean } = {}) {
  const { pollMs = awsAccountsPollMs, enabled = true } = options;

  return useQuery({
    queryKey: awsAccountsKey,
    queryFn: ({ signal }) => fetchAwsAccounts(signal),
    refetchInterval: pollMs,
    enabled,
  });
}

/** One account with the steps its connect command reported. */
export function useAwsAccount(id: string | undefined, pollMs: number) {
  return useQuery({
    queryKey: ["aws-accounts", "one", id],
    queryFn: ({ signal }) => fetchAwsAccount(id ?? "", signal),
    enabled: id !== undefined,
    refetchInterval: pollMs,
  });
}
