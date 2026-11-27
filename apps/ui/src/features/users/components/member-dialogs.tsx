import { useEffect, useId, useState, type FormEvent, type ReactNode } from "react";
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
import { Field } from "@/components/ui/field";
import { InlineError } from "@/components/ui/inline-error";
import { NativeSelect } from "@/components/ui/native-select";
import { useSession, type Role } from "@/features/auth";
import { useTeams } from "@/features/teams";
import { useRemoveMember, useUpdateMember } from "../hooks/use-users";
import { roleLabel } from "../role-label";
import type { Member } from "../types";

type MemberDialogProps = {
  member: Member;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

type Write = {
  isPending: boolean;
  error: { message: string } | null;
  reset: () => void;
};

/**
 * The frame the three dialogs share. A reopened dialog starts with no error
 * from the last try, and the control plane's refusal, such as the one for the
 * last admin, is shown as it stands.
 */
function MemberDialog({
  open,
  onOpenChange,
  title,
  description,
  write,
  submit,
  onSubmit,
  destructive = false,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  write: Write;
  submit: { label: string; busy: string; disabled?: boolean };
  onSubmit: () => void;
  destructive?: boolean;
  children?: ReactNode;
}) {
  const reset = write.reset;
  useEffect(() => {
    if (open) {
      reset();
    }
  }, [open, reset]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        // Esc is kept for the dialogs that only change something. Removing a
        // person is a decision, so Cancel is how it is left.
        onEscapeKeyDown={destructive ? (event) => event.preventDefault() : undefined}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription className={destructive ? "text-foreground" : undefined}>
            {description}
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event: FormEvent) => {
            event.preventDefault();
            onSubmit();
          }}
        >
          {children}

          <InlineError message={write.error?.message} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={write.isPending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              variant={destructive ? "destructive" : "default"}
              disabled={submit.disabled}
              loading={write.isPending}
            >
              {write.isPending ? submit.busy : submit.label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Moves a User to another Team of the Organization. */
export function MoveMemberDialog({ member, open, onOpenChange }: MemberDialogProps) {
  const fieldId = useId();
  const teams = useTeams();
  const update = useUpdateMember(member.id);
  const others = teams.data?.map(({ name }) => name).filter((name) => name !== member.team) ?? [];
  const [chosen, setChosen] = useState("");
  const target = others.includes(chosen) ? chosen : (others[0] ?? "");

  return (
    <MemberDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Move ${member.name}`}
      description={
        <>
          {member.name} is in {member.team}. After the move they work with the environments and
          databases of the new team, from their next request.
        </>
      }
      write={update}
      submit={{ label: "Move", busy: "Moving...", disabled: target === "" }}
      onSubmit={() =>
        update.mutate(
          { team: target },
          {
            onSuccess: () => {
              toast.success(`${member.name} moved to ${target}`);
              onOpenChange(false);
            },
          },
        )
      }
    >
      {teams.isPending ? null : others.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          There is no other team to move them to. Create one on the Teams page first.
        </p>
      ) : (
        <Field id={`${fieldId}-team`} label="New team" help="Where they work from now on.">
          {(props) => (
            <NativeSelect
              {...props}
              className="w-full"
              value={target}
              disabled={update.isPending}
              onChange={(event) => setChosen(event.target.value)}
            >
              {others.map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </NativeSelect>
          )}
        </Field>
      )}
    </MemberDialog>
  );
}

const roleHelp: Record<Role, string> = {
  user: "Works with the environments and databases of their team.",
  admin: "Also manages the organization, its teams and its users.",
};

/** Makes a User an admin, or an admin a User. */
export function ChangeRoleDialog({ member, open, onOpenChange }: MemberDialogProps) {
  const fieldId = useId();
  const update = useUpdateMember(member.id);
  const [role, setRole] = useState<Role>(member.role);

  useEffect(() => {
    if (open) {
      setRole(member.role);
    }
  }, [open, member.role]);

  return (
    <MemberDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Change the role of ${member.name}`}
      description={`${member.name} is ${member.role === "admin" ? "an admin" : "a user"} now.`}
      write={update}
      submit={{ label: "Save role", busy: "Saving...", disabled: role === member.role }}
      onSubmit={() =>
        update.mutate(
          { role },
          {
            onSuccess: () => {
              toast.success(`${member.name} is ${role === "admin" ? "an admin" : "a user"} now`);
              onOpenChange(false);
            },
          },
        )
      }
    >
      <fieldset className="space-y-3" disabled={update.isPending}>
        <legend className="sr-only">Role</legend>
        {(["user", "admin"] as const).map((option) => (
          <label
            key={option}
            htmlFor={`${fieldId}-${option}`}
            className="flex cursor-pointer items-start gap-3 rounded-md border border-border p-3 has-[:checked]:border-primary"
          >
            <input
              id={`${fieldId}-${option}`}
              type="radio"
              name={`${fieldId}-role`}
              value={option}
              checked={role === option}
              onChange={() => setRole(option)}
              className="mt-0.5 size-4 accent-primary"
            />
            <span className="space-y-0.5">
              <span className="block text-sm font-medium">{roleLabel(option)}</span>
              <span className="block text-xs text-muted-foreground">{roleHelp[option]}</span>
            </span>
          </label>
        ))}
      </fieldset>
    </MemberDialog>
  );
}

/** Takes a User out of the Organization. */
export function RemoveMemberDialog({ member, open, onOpenChange }: MemberDialogProps) {
  const remove = useRemoveMember(member.id);
  const organization = useSession().data?.organization ?? "the organization";

  return (
    <MemberDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Remove ${member.name}`}
      description={`${member.name} loses access to ${organization} on their next request. Their team keeps its environments and databases, and you can invite them again.`}
      write={remove}
      submit={{ label: "Remove", busy: "Removing..." }}
      destructive
      onSubmit={() =>
        remove.mutate(undefined, {
          onSuccess: () => {
            toast.success(`${member.name} removed`);
            onOpenChange(false);
          },
        })
      }
    />
  );
}
