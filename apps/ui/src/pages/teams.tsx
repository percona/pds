import { Plus } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { TeamList, TeamNameDialog, teamPath } from "@/features/teams";

export function TeamsPage() {
  const [creating, setCreating] = useState(false);
  const navigate = useNavigate();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Teams"
        description="Teams group the users of your organization and the environments they connect. A team's users work with its environments and the databases on them."
        actions={
          <Button onClick={() => setCreating(true)}>
            <Plus className="size-4" strokeWidth={1.5} aria-hidden="true" />
            Create team
          </Button>
        }
      />

      <TeamList />

      <TeamNameDialog
        open={creating}
        onOpenChange={setCreating}
        onSaved={(name) => void navigate(teamPath(name))}
      />
    </div>
  );
}
