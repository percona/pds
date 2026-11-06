import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { InlineError } from "@/components/ui/inline-error";
import { useRenameOrganization } from "../hooks/use-organizations";
import { useSession } from "../hooks/use-session";
import { isOrganizationNameReady, OrganizationNameField } from "./organization-name-field";

/** Renames the Organization the person acts in. It starts from the current name. */
export function OrganizationNameForm() {
  const current = useSession().data?.organization ?? "";
  // Until the person types, the field shows the name the session carries, so a
  // rename elsewhere or the session arriving late is never hidden by stale state.
  const [draft, setDraft] = useState<string>();
  const rename = useRenameOrganization();
  const name = draft ?? current;
  const trimmed = name.trim();
  const ready = isOrganizationNameReady(name) && trimmed !== current;

  return (
    <form
      className="max-w-md space-y-6"
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        rename.mutate(trimmed, {
          onSuccess: () => {
            toast.success("Organization renamed");
            setDraft(undefined);
          },
        });
      }}
    >
      <OrganizationNameField
        value={name}
        onChange={setDraft}
        help="Up to 200 characters. Everyone in the organization sees it."
      />

      <InlineError message={rename.error?.message} />

      <Button type="submit" disabled={!ready} loading={rename.isPending}>
        {rename.isPending ? "Saving..." : "Save"}
      </Button>
    </form>
  );
}
