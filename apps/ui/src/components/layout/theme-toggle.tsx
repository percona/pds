import { Moon, Sun } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useTheme } from "@/lib/theme";

export function ThemeToggle({ className }: { className?: string }) {
  const { theme, toggleTheme } = useTheme();
  const nextTheme = theme === "dark" ? "light" : "dark";

  return (
    <Button
      variant="ghost"
      size="icon"
      className={className}
      onClick={toggleTheme}
      aria-label={`Switch to ${nextTheme} theme`}
    >
      {theme === "dark" ? (
        <Sun strokeWidth={1.5} aria-hidden="true" />
      ) : (
        <Moon strokeWidth={1.5} aria-hidden="true" />
      )}
    </Button>
  );
}
