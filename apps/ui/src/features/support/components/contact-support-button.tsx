import { LifeBuoy } from "lucide-react";
import { useState } from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { useSupportAvailability } from "../hooks/use-support-availability";
import { supportDraft, type SupportDraftInput } from "../support-draft";
import { ContactSupportDialog } from "./contact-support-dialog";

type ContactSupportButtonProps = SupportDraftInput &
  Pick<ButtonProps, "variant" | "size" | "className">;

/**
 * Contact support with the case already filled in from what the page knows: a
 * database, its environment, an alert, or the reason a create failed. Without
 * a ServiceNow connection the dialog says so and points at Connectors.
 */
export function ContactSupportButton({
  variant = "outline",
  size,
  className,
  ...details
}: ContactSupportButtonProps) {
  const [open, setOpen] = useState(false);
  const support = useSupportAvailability();
  const draft = supportDraft(details);

  return (
    <>
      <Button
        type="button"
        variant={variant}
        size={size}
        className={className}
        onClick={() => setOpen(true)}
      >
        <LifeBuoy strokeWidth={1.5} aria-hidden="true" />
        Contact support
      </Button>
      <ContactSupportDialog
        open={open}
        onOpenChange={setOpen}
        subject={draft.subject}
        description={draft.description}
        context={draft.context}
        connected={support.connected}
        portalUrl={support.portalUrl}
      />
    </>
  );
}
