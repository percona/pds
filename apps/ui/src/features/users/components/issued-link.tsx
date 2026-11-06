import { Check, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import type { IssuedInvitation } from "../types";

/**
 * Says what became of an invite and shows its link. The control plane keeps
 * only a hash of the link, so this is the one chance to copy it.
 */
export function IssuedLink({ invitation }: { invitation: IssuedInvitation }) {
  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(invitation.link);
      toast.success("Link copied");
    } catch {
      toast.error("The browser blocked the copy. Select the link and copy it.");
    }
  }

  return (
    <Card role="status" className="space-y-3 p-4">
      <p className="flex items-start gap-2 text-sm font-medium">
        <Check
          className="mt-0.5 size-4 shrink-0 text-success"
          strokeWidth={1.5}
          aria-hidden="true"
        />
        {headline(invitation)}
      </p>
      <div className="flex items-center gap-2">
        <code className="min-w-0 flex-1 truncate rounded-md border border-border bg-muted px-3 py-2 font-mono text-xs">
          {invitation.link}
        </code>
        <Button variant="outline" onClick={() => void copy()}>
          <Copy className="size-4" strokeWidth={1.5} aria-hidden="true" />
          Copy link
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        This link is shown once. Resend the invite to get a new one.
      </p>
    </Card>
  );
}

function headline({ email, emailed }: IssuedInvitation): string {
  if (email === undefined || email === "") {
    return "Invite created for anyone. Send them this link.";
  }

  return emailed
    ? `Invite sent to ${email}.`
    : `Invite created for ${email}. It was not emailed, so send them this link.`;
}
