import { Plus } from "lucide-react";
import { useId, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import type { Environment } from "@/features/environments";
import { createDatabasePath, environmentPhase, takesDatabases } from "../environment-phase";

type CreateDatabaseButtonProps = {
  environments: Environment[];
  /** How many databases each environment runs, by name. */
  databaseCounts: Map<string, number>;
};

/**
 * Create database from Home, where no environment is in context. One environment
 * that can take a database goes straight to its form; several ask which first.
 */
export function CreateDatabaseButton({ environments, databaseCounts }: CreateDatabaseButtonProps) {
  const choices = environments.filter(takesDatabases);
  const [open, setOpen] = useState(false);

  if (choices.length === 0) {
    return (
      <Button disabled title="No environment can take a database right now">
        <Plus strokeWidth={1.5} aria-hidden="true" />
        Create database
      </Button>
    );
  }

  if (choices.length === 1) {
    return (
      <Button asChild>
        <Link to={createDatabasePath(choices[0].name)}>
          <Plus strokeWidth={1.5} aria-hidden="true" />
          Create database
        </Link>
      </Button>
    );
  }

  return (
    <>
      <Button onClick={() => setOpen(true)}>
        <Plus strokeWidth={1.5} aria-hidden="true" />
        Create database
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <EnvironmentPicker choices={choices} databaseCounts={databaseCounts} />
        </DialogContent>
      </Dialog>
    </>
  );
}

function EnvironmentPicker({
  choices,
  databaseCounts,
}: {
  choices: Environment[];
  databaseCounts: Map<string, number>;
}) {
  const navigate = useNavigate();
  const name = useId();
  const [picked, setPicked] = useState(choices[0].name);

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void navigate(createDatabasePath(picked));
  }

  return (
    <form onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Create database</DialogTitle>
        <DialogDescription>Choose the environment it runs on.</DialogDescription>
      </DialogHeader>
      <fieldset className="space-y-2">
        <legend className="sr-only">Environment</legend>
        {choices.map((environment) => {
          const count = databaseCounts.get(environment.name) ?? 0;
          const quiet = environmentPhase(environment) === "unreachable";

          return (
            <label
              key={environment.name}
              className="flex cursor-pointer items-start gap-3 rounded-md border border-border px-3 py-2 has-[:checked]:border-primary"
            >
              <input
                type="radio"
                name={name}
                value={environment.name}
                checked={picked === environment.name}
                onChange={() => setPicked(environment.name)}
                className="mt-1 accent-primary"
              />
              <span className="min-w-0 space-y-0.5">
                <span className="block truncate font-mono text-sm font-medium">
                  {environment.name}
                </span>
                <span className="block text-xs text-muted-foreground">
                  {environment.team ? `${environment.team}, ` : ""}
                  <span className="font-mono">{count}</span>
                  {count === 1 ? " database" : " databases"}
                  {quiet ? ", not reporting" : ""}
                </span>
              </span>
            </label>
          );
        })}
      </fieldset>
      <DialogFooter>
        <DialogClose asChild>
          <Button type="button" variant="outline">
            Cancel
          </Button>
        </DialogClose>
        <Button type="submit">Continue</Button>
      </DialogFooter>
    </form>
  );
}
