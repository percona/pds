import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { InvitationList, InviteForm, IssuedLink, MemberList } from "@/features/users";
import type { IssuedInvitation } from "@/features/users";

export function UsersPage() {
  const [issued, setIssued] = useState<IssuedInvitation | null>(null);

  return (
    <div className="space-y-8">
      <PageHeader
        title="Users"
        description="Invite people to your organization and see who belongs to it."
      />

      <section className="space-y-4" aria-labelledby="invite-heading">
        <h2 id="invite-heading" className="text-base font-semibold">
          Invite a user
        </h2>
        <InviteForm onIssued={setIssued} />
        {issued ? <IssuedLink invitation={issued} /> : null}
      </section>

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
