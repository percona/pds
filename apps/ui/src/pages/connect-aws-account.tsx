import { Link } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { ConnectAccountPanel } from "@/features/aws-accounts";
import { useTeamToCreateIn } from "@/features/teams";
import { useState } from "react";
import type { AwsAccount } from "@/features/aws-accounts";

/**
 * Where the console and the agents' start_cloud_connection tool send a person to
 * connect an AWS account: one command, with its progress beside it.
 */
export function ConnectAwsAccountPage() {
  const inTeam = useTeamToCreateIn();
  const [connected, setConnected] = useState<AwsAccount | null>(null);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Connect an AWS account"
        description="Connect an AWS account so PDS can create environments in it."
      />
      <Card className="space-y-4 px-5 py-5">
        {inTeam.field}
        <ConnectAccountPanel team={inTeam.team} onConnected={setConnected} />
      </Card>
      {connected ? (
        <div className="flex items-center gap-3">
          <Button asChild>
            <Link to={`/environments/new?account=${encodeURIComponent(connected.id)}`}>
              Create an environment
            </Link>
          </Button>
          <Button asChild variant="outline">
            <Link to="/environments">Back to environments</Link>
          </Button>
        </div>
      ) : null}
    </div>
  );
}
