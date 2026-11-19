import { CircleCheck } from "lucide-react";
import { cn } from "@/lib/utils";

export type Choice<T extends string> = {
  id: T;
  label: string;
  detail?: string;
  logo?: string;
  ariaLabel?: string;
};

type ChoiceGroupProps<T extends string> = {
  legend: string;
  name: string;
  choices: Choice<T>[];
  value: T;
  onChange: (value: T) => void;
  className?: string;
};

/**
 * A radio group drawn as cards. The inputs are native, so the arrow keys move
 * the choice and Tab enters the group once. The selected card shows a check as
 * well as a border, so the choice never rests on color alone.
 */
export function ChoiceGroup<T extends string>({
  legend,
  name,
  choices,
  value,
  onChange,
  className,
}: ChoiceGroupProps<T>) {
  return (
    <fieldset className="min-w-0 space-y-1.5 sm:space-y-2">
      <legend className="text-xs font-medium text-muted-foreground">{legend}</legend>
      <div className={cn("grid gap-2", className)}>
        {choices.map((choice) => (
          <label key={choice.id} className="relative block cursor-pointer">
            <input
              type="radio"
              name={name}
              value={choice.id}
              checked={choice.id === value}
              onChange={() => onChange(choice.id)}
              className="peer sr-only"
            />
            <span
              className={cn(
                "flex h-full min-h-11 items-center gap-2 rounded-md border border-input bg-card px-3 py-2 text-sm transition-colors",
                "hover:bg-accent peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary",
                "peer-checked:border-primary peer-checked:bg-accent",
              )}
            >
              <CircleCheck
                className={cn(
                  "size-4 shrink-0",
                  choice.id === value ? "text-primary" : "text-transparent",
                )}
                strokeWidth={1.5}
                aria-hidden="true"
              />
              {choice.logo ? (
                // The marks are white, so they sit on a fixed dark tile in both themes.
                <span className="grid size-8 shrink-0 place-items-center rounded-md bg-mark-tile p-1">
                  <img src={choice.logo} alt="" width={24} height={24} className="size-6" />
                </span>
              ) : null}
              <span className="min-w-0">
                <span className="block font-medium">{choice.label}</span>
                {choice.detail ? (
                  <span className="hidden text-xs text-muted-foreground sm:block">
                    {choice.detail}
                  </span>
                ) : null}
              </span>
            </span>
          </label>
        ))}
      </div>
    </fieldset>
  );
}
