import type { AiAccess } from "./types";

export const accessLabels: Record<AiAccess, string> = {
  read: "Read only",
  change: "Read and change",
  change_delete: "Read, change and delete",
};

export const accessChoices: { id: AiAccess; hint: string }[] = [
  { id: "read", hint: "List databases and read metrics, alerts and logs." },
  { id: "change", hint: "Also create, scale, back up and restore databases." },
  { id: "change_delete", hint: "Also delete databases." },
];
