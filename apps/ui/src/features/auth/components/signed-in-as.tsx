import { LogOut } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useSession, useSignOut } from "../hooks/use-session";

/**
 * Who is signed in, and the way out. It sits at the foot of the sidebar,
 * because that is where a customer looks to check which login they are
 * using before they delete something.
 *
 * The username is mono: it is an identifier the customer typed, and reading
 * it back in the same shape is how they recognize it.
 */
export function SignedInAs() {
  const session = useSession();
  const signOut = useSignOut();

  if (session.data == null) {
    return null;
  }

  return (
    <div className="space-y-2 border-t border-border px-4 py-3">
      <p className="text-xs text-muted-foreground">Signed in as</p>
      {session.data.user ? (
        <p className="truncate text-sm" title={session.data.user}>
          {session.data.user}
        </p>
      ) : (
        <p className="truncate font-mono text-sm" title={session.data.username}>
          {session.data.username}
        </p>
      )}
      <Button
        variant="outline"
        size="sm"
        className="w-full"
        onClick={() => signOut.mutate()}
        disabled={signOut.isPending}
        loading={signOut.isPending}
      >
        {signOut.isPending ? null : (
          <LogOut className="size-4" strokeWidth={1.5} aria-hidden="true" />
        )}
        {signOut.isPending ? "Signing out..." : "Sign out"}
      </Button>
    </div>
  );
}
