import { useParams } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { ActivityDetailView } from "@/features/activity";

/**
 * One change, from the request to whatever the environment did about it.
 *
 * It has its own route rather than a drawer so the link survives a reload and
 * can be pasted into a support case while the operation is still running.
 */
export function ActivityDetailPage() {
  const { id = "" } = useParams();

  return (
    <div className="space-y-6">
      <PageHeader
        title="Change"
        description="What was asked for, and what the platform did about it."
      />

      <ActivityDetailView id={id} />
    </div>
  );
}
