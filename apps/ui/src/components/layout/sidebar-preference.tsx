import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";

/** Remembers whether the desktop rail is collapsed. */
export const sidebarStorageKey = "pds-sidebar";

// Tailwind's `md` breakpoint is 768px. Below that the sidebar is a drawer.
const phoneQuery = "(max-width: 767px)";

export type SidebarPreference = {
  /** The desktop rail is icon-width. A phone drawer always shows labels. */
  rail: boolean;
  phone: boolean;
  drawerOpen: boolean;
  toggleCollapsed: () => void;
  openDrawer: () => void;
  closeDrawer: () => void;
};

function readCollapsed(): boolean {
  try {
    return window.localStorage.getItem(sidebarStorageKey) === "collapsed";
  } catch {
    return false;
  }
}

function writeCollapsed(collapsed: boolean) {
  try {
    window.localStorage.setItem(sidebarStorageKey, collapsed ? "collapsed" : "expanded");
  } catch {
    // Storage can be unavailable. The rail still collapses for this visit.
  }
}

function phoneSnapshot(): boolean {
  if (typeof window.matchMedia !== "function") {
    return false;
  }

  return window.matchMedia(phoneQuery).matches;
}

function subscribePhone(onStoreChange: () => void) {
  if (typeof window.matchMedia !== "function") {
    return () => {};
  }

  const media = window.matchMedia(phoneQuery);
  media.addEventListener("change", onStoreChange);

  return () => media.removeEventListener("change", onStoreChange);
}

function usePreferenceState(): SidebarPreference {
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const phone = useSyncExternalStore(subscribePhone, phoneSnapshot, () => false);

  useEffect(() => {
    if (!phone) {
      setDrawerOpen(false);
    }
  }, [phone]);

  return useMemo(
    () => ({
      rail: collapsed && !phone,
      phone,
      drawerOpen: phone && drawerOpen,
      toggleCollapsed: () => {
        setCollapsed((current) => {
          const next = !current;
          writeCollapsed(next);

          return next;
        });
      },
      openDrawer: () => setDrawerOpen(true),
      closeDrawer: () => setDrawerOpen(false),
    }),
    [collapsed, phone, drawerOpen],
  );
}

const fallbackPreference: SidebarPreference = {
  rail: false,
  phone: false,
  drawerOpen: false,
  toggleCollapsed: () => {},
  openDrawer: () => {},
  closeDrawer: () => {},
};

const SidebarPreferenceContext = createContext<SidebarPreference>(fallbackPreference);

export function SidebarPreferenceProvider({ children }: { children: ReactNode }) {
  const value = usePreferenceState();

  return (
    <SidebarPreferenceContext.Provider value={value}>{children}</SidebarPreferenceContext.Provider>
  );
}

/**
 * The shell's sidebar preference. Outside the provider the rail stays
 * expanded, which is what a chat panel rendered on its own should assume.
 */
export function useSidebarPreference(): SidebarPreference {
  return useContext(SidebarPreferenceContext);
}
