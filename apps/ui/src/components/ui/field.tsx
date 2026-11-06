import type { ReactNode } from "react";
import { Label } from "@/components/ui/label";

export type FieldRenderProps = {
  id: string;
  "aria-describedby": string;
  "aria-invalid"?: "true";
};

export type FieldProps = {
  id: string;
  label: string;
  help: string;
  error?: string;
  children: (props: FieldRenderProps) => ReactNode;
};

/**
 * One form row: label above input, help under the label, error under the
 * input. The control is a render prop so the caller owns registration and this
 * stays the layout and the accessible wiring only.
 */
export function Field({ id, label, help, error, children }: FieldProps) {
  const helpId = `${id}-help`;
  const errorId = `${id}-error`;

  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <p id={helpId} className="text-sm text-muted-foreground">
        {help}
      </p>
      {children({
        id,
        "aria-describedby": error ? `${helpId} ${errorId}` : helpId,
        "aria-invalid": error ? "true" : undefined,
      })}
      {error ? (
        <p id={errorId} className="text-sm text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}
