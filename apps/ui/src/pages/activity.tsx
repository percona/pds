import { useState } from "react";
import { PageHeader } from "@/components/layout/page-header";
import { ActivityFeed } from "@/features/activity";

/**
 * Everything the control plane did, across every database and connection.
 * The per database view is the Activity tab on the detail page; this is the
 * environment wide one.
 */
export function ActivityPage() {
  const [live, setLive] = useState(true);

  return (
    <div className="space-y-6">
      <PageHeader title="Activity" description="Every change this platform made, newest first." />

      <ActivityFeed live={live} onLiveChange={setLive} />
    </div>
  );
}
