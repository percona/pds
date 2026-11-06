import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { TeamList, TeamNameDialog } from "@/features/teams";

export function TeamsPage() {
  const [creating, setCreating] = useState(false);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Teams"
        description="Teams group the users of your organization and the environments they connect."
        actions={<Button onClick={() => setCreating(true)}>Create team</Button>}
      />

      <TeamList />

      <TeamNameDialog open={creating} onOpenChange={setCreating} />
    </div>
  );
}
