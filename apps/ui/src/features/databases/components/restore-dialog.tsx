import { CircleAlert } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
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
import { Input } from "@/components/ui/input";
import { formatRelativeTime, formatTimestamp } from "@/lib/format";
import { useDatabases } from "../hooks/use-databases";
import { useRestoreDatabase } from "../hooks/use-restore-database";
import {
  isRestoreDatabaseName,
  restoreNameRule,
  restoreNameSuggestion,
  restoreSource,
} from "../restore";
import type { Backup, Database } from "../types";

function isFailedRestoreCopy(item: Database): boolean {
  return item.engine === "postgresql" && item.status === "failed" && !item.connection;
}

type RestoreDialogProps = {
  database: Database;
  /** The backup the row was opened on. Absent means no row has been picked. */
  backup?: Backup;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

/**
 * The restore dialog. A restore creates a second database from the backup and
 * leaves the source running, so it asks for the name that database gets.
 */
export function RestoreDialog({ database, backup, open, onOpenChange }: RestoreDialogProps) {
  // The dialog is opened from a backup row, so with no row there is nothing
  // to restore and nothing to render.
  if (!backup) {
    return null;
  }

  return (
    <RestoreIntoNewDatabase
      database={database}
      backup={backup}
      environment={database.environment}
      open={open}
      onOpenChange={onOpenChange}
    />
  );
}

type FormProps = {
  database: Database;
  backup: Backup;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

function RestoreIntoNewDatabase({
  database,
  backup,
  environment,
  open,
  onOpenChange,
}: FormProps & { environment: string }) {
  const fieldId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState("");
  const [touched, setTouched] = useState(false);
  const edited = useRef(false);
  const restore = useRestoreDatabase(database.id);
  // The names on the environment decide what to offer and what to refuse, so the
  // developer is not sent to the adapter to find out a name is in use. Until
  // that read lands there is nothing to check a name against, and offering a
  // name that turns out to be taken is worse than waiting for the list.
  const { data: siblings, isError: namesUnread } = useDatabases(environment);
  const namesRead = siblings !== undefined || namesUnread;
  // A PostgreSQL copy whose seed failed has no connection and nothing committed
  // behind it, and the hub accepts its name again, so it does not hold the name.
  const taken = useMemo(
    () => (siblings ?? []).filter((item) => !isFailedRestoreCopy(item)).map((item) => item.name),
    [siblings],
  );
  const rule = restoreNameRule(database.engine);
  const suggestion = useMemo(
    () => restoreNameSuggestion(database.name, taken, database.engine),
    [database.name, taken, database.engine],
  );

  useEffect(() => {
    if (open) {
      edited.current = false;
      setTouched(false);
      restore.reset();
    }
  }, [open, backup.id, restore.reset]);

  // The suggestion arrives with the environment's databases, and moves when one of
  // them already holds the name. A name the developer typed is left alone.
  useEffect(() => {
    if (open && !edited.current) {
      setName(suggestion);
    }
  }, [open, backup.id, suggestion]);

  const trimmed = name.trim();
  const valid = isRestoreDatabaseName(trimmed, database.engine);
  const inUse = valid && taken.includes(trimmed);
  // A failed read leaves the adapter to refuse a name in use, which it does.
  const confirmed = valid && !inUse && namesRead;
  // What is wrong with the name, which is not the same question as whether it
  // can be submitted yet: a list that has not landed holds the button without
  // giving the developer anything to fix.
  const wrong = !valid
    ? trimmed.length > rule.limit
      ? `Use ${rule.limit} characters or fewer. ${rule.reason}`
      : "That is not a database name."
    : inUse
      ? `A database named ${trimmed} is already on ${environment}.`
      : undefined;

  return (
    <RestoreFrame
      title={`Restore ${database.name}`}
      backup={backup}
      open={open}
      onOpenChange={onOpenChange}
      firstField={inputRef}
      pending={restore.isPending}
      confirmed={confirmed}
      failure={restore.error?.message}
      onSubmit={() =>
        restore.mutate(
          { backupId: backup.id, name: trimmed },
          {
            onSuccess: (accepted) => {
              toast.success(`Restore started into ${accepted.name ?? trimmed}`);
              onOpenChange(false);
            },
          },
        )
      }
      consequence={
        <>
          This restores the backup into a new database on{" "}
          <span className="font-mono">{environment}</span>.{" "}
          <span className="font-mono">{database.name}</span> is not touched.
        </>
      }
    >
      <Field
        id={`${fieldId}-name`}
        label="New database name"
        help={`Lowercase letters, digits, and dashes, starting with a letter, ${rule.limit} at most. It runs next to ${database.name} on ${environment}.`}
        error={touched && trimmed !== "" ? wrong : undefined}
      >
        {(props) => (
          <Input
            {...props}
            ref={inputRef}
            value={name}
            onChange={(event) => {
              edited.current = true;
              setName(event.target.value);
            }}
            onBlur={() => setTouched(true)}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
            disabled={restore.isPending}
          />
        )}
      </Field>
    </RestoreFrame>
  );
}

type RestoreFrameProps = {
  title: string;
  backup: Backup;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  consequence: ReactNode;
  children: ReactNode;
  firstField: RefObject<HTMLInputElement | null>;
  confirmed: boolean;
  pending: boolean;
  failure?: string;
  onSubmit: () => void;
};

function RestoreFrame({
  title,
  backup,
  open,
  onOpenChange,
  consequence,
  children,
  firstField,
  confirmed,
  pending,
  failure,
  onSubmit,
}: RestoreFrameProps) {
  const started = formatRelativeTime(backup.createdAt);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="max-w-lg"
        onOpenAutoFocus={(event) => {
          event.preventDefault();
          firstField.current?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription data-testid="restore-consequence" className="text-foreground">
            {consequence}
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-6"
          noValidate
          onSubmit={(event) => {
            event.preventDefault();
            onSubmit();
          }}
        >
          <div
            data-testid="restore-source"
            className="space-y-0.5 rounded-lg border border-border px-4 py-3"
          >
            <p className="font-mono text-sm">{backup.id}</p>
            <p className="text-sm text-muted-foreground">
              {restoreSource(backup)}
              {started ? (
                <span title={formatTimestamp(backup.createdAt) ?? undefined}>
                  , started {started}
                </span>
              ) : null}
            </p>
          </div>

          {children}

          {failure ? (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-lg border border-destructive px-4 py-3 text-sm"
            >
              <CircleAlert
                className="mt-0.5 size-4 shrink-0 text-destructive"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              <span>{failure}</span>
            </div>
          ) : null}

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!confirmed || pending} loading={pending}>
              {pending ? "Restoring..." : "Restore"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
