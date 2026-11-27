import { ExternalLink, PlugZap } from "lucide-react";
import { Link } from "react-router";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { publicSupportPortalUrl } from "../portal";

/**
 * What Support shows when the control plane has no ServiceNow connection. The
 * connection is made once for the organization in Connectors, so that is where
 * it points, with the public portal as the route that works today.
 */
export function SupportNotConnected() {
  return (
    <Card
      className="flex flex-col items-center gap-3 px-6 py-12 text-center"
      data-testid="support-page-not-connected"
    >
      <PlugZap className="size-5 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
      <div className="space-y-1">
        <p className="text-sm font-medium">Support is not connected</p>
        <p className="max-w-md text-sm text-muted-foreground">
          Percona support cases open in ServiceNow. An admin connects it once for your organization
          in Connectors.
        </p>
      </div>
      <div className="flex flex-wrap justify-center gap-2">
        <Button asChild>
          <Link to="/connectors">Open Connectors</Link>
        </Button>
        <Button variant="outline" asChild>
          <a href={publicSupportPortalUrl} target="_blank" rel="noreferrer">
            Open the support portal
            <ExternalLink className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
          </a>
        </Button>
      </div>
    </Card>
  );
}
