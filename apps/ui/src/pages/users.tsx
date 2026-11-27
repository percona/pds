import { UserPlus } from "lucide-react";
import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import {
  InvitationList,
  InviteDialog,
  IssuedLinks,
  MemberList,
  useInviteOutcomes,
} from "@/features/users";

export function UsersPage() {
  const [inviting, setInviting] = useState(false);
  const { outcomes, add, addIssued, dismiss } = useInviteOutcomes();

  return (
    <div className="space-y-8">
      <PageHeader
        title="Users"
        description="Invite people to your organization and see who belongs to it. A user works in one team, and an admin also manages the organization."
        actions={
          <Button type="button" onClick={() => setInviting(true)}>
            <UserPlus className="size-4" strokeWidth={1.5} aria-hidden="true" />
            Invite people
          </Button>
        }
      />

      <InviteDialog open={inviting} onOpenChange={setInviting} onSent={add} />

      <IssuedLinks outcomes={outcomes} onDismiss={dismiss} />

      <section className="space-y-4" aria-labelledby="pending-heading">
        <h2 id="pending-heading" className="text-base font-semibold">
          Pending invites
        </h2>
        <InvitationList onIssued={addIssued} />
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
