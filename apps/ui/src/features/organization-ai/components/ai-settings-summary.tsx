import { Check, Minus } from "lucide-react";
import { Link } from "react-router";
import { useIsAdmin } from "@/features/auth";
import { accessLabels } from "../choices";
import { useAiSettings } from "../hooks/use-ai-settings";

function Allowed({ label, allowed }: { label: string; allowed: boolean }) {
  const Icon = allowed ? Check : Minus;

  return (
    <li className="flex items-center gap-2">
      <Icon className="size-4" strokeWidth={1.5} aria-hidden="true" />
      {`${label}: ${allowed ? "allowed" : "not allowed"}`}
    </li>
  );
}

/**
 * What the organization lets agents and the Chat do, for any member to read.
 * It shows nothing until the control plane answers, and nothing when it cannot.
 */
export function AiSettingsSummary() {
  const admin = useIsAdmin();
  const { data } = useAiSettings();
  const label = data ? accessLabels[data.access] : undefined;
  if (!data || !label) {
    return null;
  }

  return (
    <section
      aria-labelledby="ai-summary-title"
      className="space-y-2 rounded-lg border border-border p-4 text-sm"
    >
      <h2 id="ai-summary-title" className="text-base font-semibold">
        What your organization allows
      </h2>
      <p>
        Agents and the Chat may do at most: <span className="font-medium">{label}</span>.
      </p>
      <ul className="space-y-1">
        <Allowed label="Fetch database passwords" allowed={data.passwords} />
        <Allowed label="Change data with SQL" allowed={data.sqlWrites} />
      </ul>
      {admin ? (
        <Link to="/settings" className="inline-block font-medium text-primary underline">
          Change in settings
        </Link>
      ) : (
        <p className="text-muted-foreground">An organization admin sets this.</p>
      )}
    </section>
  );
}
