import { useSyncExternalStore } from "react";

function subscribe(onChange: () => void) {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  return () => observer.disconnect();
}

function read(): "light" | "dark" {
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

/** The page's theme as it changes, for the demos whose ink tokens follow it. */
export function usePageTheme(): "light" | "dark" {
  return useSyncExternalStore(subscribe, read, () => "light");
}
