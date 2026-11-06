import type { Role } from "@/features/auth";

export function roleLabel(role: Role): string {
  return role === "admin" ? "Admin" : "User";
}
