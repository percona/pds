import { useActivity } from "./use-activity";

export type ActivityAvailability = {
  /** True once the adapter has answered with something other than a 501. */
  connected: boolean;
  /** True while the first read is still in flight. */
  checking: boolean;
};

/**
 * Whether this environment has the activity ring enabled. Only a 501 hides the
 * activity nav entry: that is the adapter stating it was built without the
 * ring. A network error leaves the entry in place, because a transient failure
 * is not a reason to remove navigation.
 */
export function useActivityAvailability(): ActivityAvailability {
  const { error, isPending } = useActivity({ live: false });

  const notConnected = error?.code === "ErrNotImplemented";

  return {
    connected: !notConnected,
    checking: isPending,
  };
}
