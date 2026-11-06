import { useCallback, useState } from "react";

export type Theme = "light" | "dark";

/** Shared with the console, so a visitor's choice carries across both. */
export const themeStorageKey = "pds-theme";

function currentTheme(): Theme {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

/**
 * The theme lives on the `dark` class of <html>, set before paint by the
 * inline script in index.html. This hook only reads and flips it.
 */
export function useTheme() {
  const [theme, setTheme] = useState<Theme>(currentTheme);

  const toggleTheme = useCallback(() => {
    const next: Theme = currentTheme() === "dark" ? "light" : "dark";
    document.documentElement.classList.toggle("dark", next === "dark");
    try {
      localStorage.setItem(themeStorageKey, next);
    } catch {
      // Storage can be blocked; the toggle still works for this visit.
    }
    setTheme(next);
  }, []);

  return { theme, toggleTheme };
}
