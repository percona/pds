import { useId } from "react";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";

/** The width of the column the control plane keeps the name in. */
export const maxOrganizationNameLength = 200;

/** The sentence for a name that cannot be used, or undefined for one that can. */
export function organizationNameProblem(name: string): string | undefined {
  if (name.trim().length > maxOrganizationNameLength) {
    return `Use ${maxOrganizationNameLength} characters or fewer.`;
  }

  return undefined;
}

/** Whether a name is there and fits, so what waits on it can go ahead. */
export function isOrganizationNameReady(name: string): boolean {
  return name.trim() !== "" && organizationNameProblem(name) === undefined;
}

type OrganizationNameFieldProps = {
  value: string;
  onChange: (value: string) => void;
  help: string;
  autoFocus?: boolean;
};

/** The one field signing up and creating another organization both ask for. */
export function OrganizationNameField({
  value,
  onChange,
  help,
  autoFocus,
}: OrganizationNameFieldProps) {
  const id = useId();
  const problem = organizationNameProblem(value);

  return (
    <Field id={`${id}-name`} label="Organization name" help={help} error={problem}>
      {(props) => (
        <Input
          {...props}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          autoComplete="organization"
          autoFocus={autoFocus}
        />
      )}
    </Field>
  );
}
