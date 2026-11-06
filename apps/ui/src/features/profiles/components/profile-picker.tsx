import { useId } from "react";
import { cn } from "@/lib/utils";
import { profiles } from "../profiles";
import type { ProfileId } from "../profiles";

type ProfilePickerProps = {
  value: ProfileId;
  onChange: (id: ProfileId) => void;
  disabled?: boolean;
};

/**
 * The three profile cards the create form opens with. Native radios carry the
 * keyboard behavior; `aria-labelledby` keeps each radio named after the
 * profile rather than the whole card.
 */
export function ProfilePicker({ value, onChange, disabled }: ProfilePickerProps) {
  const groupId = useId();

  return (
    <div className="grid gap-3 sm:grid-cols-3">
      {profiles.map((profile) => (
        <ProfileCard
          key={profile.id}
          profile={profile}
          groupId={groupId}
          checked={profile.id === value}
          disabled={disabled}
          onSelect={onChange}
        />
      ))}
    </div>
  );
}

type ProfileCardProps = {
  profile: (typeof profiles)[number];
  groupId: string;
  checked: boolean;
  disabled?: boolean;
  onSelect: (id: ProfileId) => void;
};

function ProfileCard({ profile, groupId, checked, disabled, onSelect }: ProfileCardProps) {
  const inputId = `${groupId}-${profile.id}`;
  const labelId = `${inputId}-label`;
  const detailId = `${inputId}-detail`;

  return (
    <label
      htmlFor={inputId}
      data-testid={`profile-card-${profile.id}`}
      className={cn(
        "flex cursor-pointer flex-col gap-1 rounded-lg border bg-card p-4 transition-colors",
        "has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
        checked ? "border-primary" : "border-border hover:bg-accent",
        disabled && "cursor-not-allowed opacity-50",
      )}
    >
      <input
        id={inputId}
        type="radio"
        name={groupId}
        value={profile.id}
        checked={checked}
        disabled={disabled}
        onChange={() => onSelect(profile.id)}
        aria-labelledby={labelId}
        aria-describedby={detailId}
        className="sr-only"
      />
      <span id={labelId} className="text-sm font-medium">
        {profile.label}
      </span>
      <span id={detailId} className="text-sm text-muted-foreground">
        {profile.consequence}
      </span>
      <span className="font-mono text-xs text-muted-foreground">
        {cores(profile.cpuCores)}, {profile.memoryGb} GB memory, {profile.storageGb} GB storage
      </span>
    </label>
  );
}

function cores(count: number): string {
  return count === 1 ? "1 core" : `${count} cores`;
}
