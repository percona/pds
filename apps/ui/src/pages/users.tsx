import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InvitationList, InviteForm, IssuedLink, MemberList } from "@/features/users";
import type { IssuedInvitation } from "@/features/users";

export function UsersPage() {
  const [issued, setIssued] = useState<IssuedInvitation | null>(null);
  const [inviting, setInviting] = useState(false);

  return (
    <div className="space-y-8">
      <PageHeader
        title="Users"
        description="Invite people to your organization and see who belongs to it."
        actions={
          <Button type="button" onClick={() => setInviting(true)}>
            Invite people
          </Button>
        }
      />

      <Dialog open={inviting} onOpenChange={setInviting}>
        <DialogContent className="max-h-[calc(100vh-4rem)] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Invite people</DialogTitle>
            <DialogDescription>
              Each address gets its own invitation. The link is shown once, after it is sent.
            </DialogDescription>
          </DialogHeader>
          <InviteForm
            onIssued={(invitation) => {
              setIssued(invitation);
              setInviting(false);
            }}
          />
        </DialogContent>
      </Dialog>

      {issued ? <IssuedLink invitation={issued} /> : null}

      <section className="space-y-4" aria-labelledby="pending-heading">
        <h2 id="pending-heading" className="text-base font-semibold">
          Pending invites
        </h2>
        <InvitationList onIssued={setIssued} />
      </section>

      <section className="space-y-4" aria-labelledby="users-heading">
        <h2 id="users-heading" className="text-base font-semibold">
          Users
        </h2>
        <MemberList />
      </section>
    </div>
  );
}
