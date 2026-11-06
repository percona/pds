import { PageHeader } from "@/components/layout/page-header";
import { OrganizationNameForm } from "@/features/auth";
import { AiSettingsForm } from "@/features/organization-ai";

export function SettingsPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Settings" description="Settings for your organization." />

      <OrganizationNameForm />

      <section className="space-y-4 border-t border-border pt-6">
        <h2 className="text-base font-semibold">AI agents and the Chat</h2>
        <AiSettingsForm />
      </section>
    </div>
  );
}
