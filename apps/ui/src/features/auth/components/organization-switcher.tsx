import { Building2, Check, ChevronsUpDown } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { InlineError } from "@/components/ui/inline-error";
import { cn } from "@/lib/utils";
import { useCreateOrganization, useSwitchOrganization } from "../hooks/use-organizations";
import { useSession } from "../hooks/use-session";
import type { OrganizationSummary } from "../types";
import { isOrganizationNameReady, OrganizationNameField } from "./organization-name-field";

/**
 * Which organization the console is acting in. Only an admin, or a person in
 * several organizations, has anything to do here, so everybody else gets the
 * name as a label. A session that cannot switch (it has no list to switch
 * between) gets the label too.
 */
export function OrganizationSwitcher() {
  const session = useSession().data;
  const [open, setOpen] = useState(false);

  if (session?.organization == null) {
    return null;
  }

  const organizations = session.organizations ?? [];
  const switchable =
    organizations.length > 0 && (session.role === "admin" || organizations.length > 1);

  if (!switchable) {
    return (
      <p className="flex items-center gap-2 px-2.5 py-2 text-sm font-medium" title="Organization">
        <Building2 className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        <span className="truncate">{session.organization}</span>
      </p>
    );
  }

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        className="w-full justify-between px-2.5"
        aria-haspopup="dialog"
        onClick={() => setOpen(true)}
      >
        <span className="flex min-w-0 items-center gap-2">
          <Building2 className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
          <span className="truncate">{session.organization}</span>
        </span>
        <ChevronsUpDown
          className="size-4 shrink-0 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
      </Button>
      <SwitchDialog open={open} onOpenChange={setOpen} organizations={organizations} />
    </>
  );
}

type SwitchDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  organizations: OrganizationSummary[];
};

function SwitchDialog({ open, onOpenChange, organizations }: SwitchDialogProps) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const navigate = useNavigate();
  const switchTo = useSwitchOrganization();
  const create = useCreateOrganization();
  const failure = switchTo.error ?? create.error;
  const pending = switchTo.isPending || create.isPending;

  const close = (next: boolean) => {
    if (!next) {
      setCreating(false);
      setName("");
      switchTo.reset();
      create.reset();
    }
    onOpenChange(next);
  };

  // What was on screen belonged to the organization the session just left, so
  // the console starts again from home.
  const arrived = (message: string) => {
    toast.success(message);
    close(false);
    void navigate("/");
  };

  const ready = isOrganizationNameReady(name);

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{creating ? "Create organization" : "Switch organization"}</DialogTitle>
          <DialogDescription>
            {creating
              ? "You become its admin. Your organizations stay as they are."
              : "Pick the organization to work in."}
          </DialogDescription>
        </DialogHeader>

        {creating ? (
          <form
            className="space-y-6"
            noValidate
            onSubmit={(event) => {
              event.preventDefault();
              create.mutate(name.trim(), { onSuccess: () => arrived(`Created ${name.trim()}`) });
            }}
          >
            <OrganizationNameField
              value={name}
              onChange={setName}
              help="Your company or team."
              autoFocus
            />
            <InlineError message={failure?.message} />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={pending}
                onClick={() => setCreating(false)}
              >
                Back
              </Button>
              <Button type="submit" disabled={!ready} loading={create.isPending}>
                {create.isPending ? "Creating..." : "Create organization"}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div className="space-y-4">
            <ul className="divide-y divide-border rounded-lg border border-border">
              {organizations.map((organization) => (
                <li key={organization.id}>
                  <button
                    type="button"
                    aria-current={organization.current ? "true" : undefined}
                    disabled={pending}
                    onClick={() =>
                      organization.current
                        ? close(false)
                        : switchTo.mutate(organization.id, {
                            onSuccess: () => arrived(`Switched to ${organization.name}`),
                          })
                    }
                    className={cn(
                      "flex w-full items-center justify-between gap-3 px-3 py-2.5 text-left text-sm",
                      "hover:bg-accent disabled:opacity-60",
                    )}
                  >
                    <span className="min-w-0">
                      <span className="block truncate font-medium">{organization.name}</span>
                      <span className="block text-xs text-muted-foreground">
                        {organization.role === "admin" ? "Admin" : "User"}
                      </span>
                    </span>
                    {organization.current ? (
                      <span className="flex items-center gap-1 text-xs text-muted-foreground">
                        <Check className="size-4" strokeWidth={1.5} aria-hidden="true" />
                        Current
                      </span>
                    ) : null}
                  </button>
                </li>
              ))}
            </ul>
            <InlineError message={failure?.message} />
            <Button
              type="button"
              variant="outline"
              className="w-full"
              onClick={() => setCreating(true)}
            >
              Create organization
            </Button>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
