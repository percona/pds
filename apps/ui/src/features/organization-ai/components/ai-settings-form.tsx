import { useId, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { InlineError } from "@/components/ui/inline-error";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import { accessChoices, accessLabels } from "../choices";
import { useAiSettings, useSaveAiSettings } from "../hooks/use-ai-settings";
import type { AiSettings } from "../types";

type SwitchProps = {
  id: string;
  label: string;
  help: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
};

function Switch({ id, label, help, checked, onChange }: SwitchProps) {
  return (
    <div className="flex items-start gap-2">
      <input
        id={id}
        type="checkbox"
        className="mt-0.5 size-4 accent-primary"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      <div className="space-y-0.5">
        <Label htmlFor={id}>{label}</Label>
        <p className="text-sm text-muted-foreground">{help}</p>
      </div>
    </div>
  );
}

/** Where an admin sets what agents and the Chat may do. It starts from what is saved. */
export function AiSettingsForm() {
  const id = useId();
  const saved = useAiSettings();
  const save = useSaveAiSettings();
  // Until the admin changes something the form shows what is saved, so a late
  // answer is never hidden by stale state.
  const [draft, setDraft] = useState<AiSettings>();

  if (saved.isPending) {
    return <Skeleton className="h-48 w-full max-w-md" />;
  }
  if (saved.isError) {
    return <InlineError message={saved.error.message} />;
  }

  const current = draft ?? saved.data;
  const edit = (next: AiSettings) => {
    save.reset();
    setDraft(next);
  };
  const changed =
    current.access !== saved.data.access ||
    current.passwords !== saved.data.passwords ||
    current.sqlWrites !== saved.data.sqlWrites;

  return (
    <form
      className="max-w-md space-y-6"
      onSubmit={(event) => {
        event.preventDefault();
        save.mutate(current, {
          onSuccess: () => {
            toast.success("AI settings saved");
            setDraft(undefined);
          },
        });
      }}
    >
      <fieldset className="space-y-2">
        <legend id={`${id}-access`} className="text-sm font-medium">
          The most agents and the Chat may do
        </legend>
        <p className="text-sm text-muted-foreground">
          Each person is also held to what their own role allows. A lower limit applies to running
          agent sessions at their next request.
        </p>
        <div role="radiogroup" aria-labelledby={`${id}-access`} className="space-y-2">
          {accessChoices.map((choice) => (
            <label
              key={choice.id}
              className={cn(
                "flex cursor-pointer flex-col rounded-lg border p-3 text-sm",
                "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
                current.access === choice.id ? "border-primary bg-accent" : "border-border",
              )}
            >
              <span className="flex items-center gap-2 font-medium">
                <input
                  type="radio"
                  name={`${id}-access`}
                  className="size-4 accent-primary"
                  checked={current.access === choice.id}
                  onChange={() => edit({ ...current, access: choice.id })}
                />
                {accessLabels[choice.id]}
              </span>
              <span className="pl-6 text-muted-foreground">{choice.hint}</span>
            </label>
          ))}
        </div>
      </fieldset>

      <div className="space-y-3">
        <Switch
          id={`${id}-passwords`}
          label="Let local agents fetch database passwords"
          help="Applies to agents that run on a person's machine. It is off until you turn it on."
          checked={current.passwords}
          onChange={(passwords) => edit({ ...current, passwords })}
        />
        <Switch
          id={`${id}-sql`}
          label="Let local agents change data with SQL"
          help="Applies to agents that run on a person's machine. It is off until you turn it on."
          checked={current.sqlWrites}
          onChange={(sqlWrites) => edit({ ...current, sqlWrites })}
        />
      </div>

      <InlineError message={save.error?.message} />

      <Button type="submit" disabled={!changed} loading={save.isPending}>
        {save.isPending ? "Saving..." : "Save changes"}
      </Button>
    </form>
  );
}
