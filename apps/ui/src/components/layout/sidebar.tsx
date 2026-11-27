import {
  Bell,
  Bot,
  Building2,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  LifeBuoy,
  PanelLeftClose,
  PanelLeftOpen,
  ScrollText,
  Server,
  Settings,
  UserPlus,
  Users,
  X,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { ReactNode, RefObject } from "react";
import { Link, NavLink } from "react-router";
import { ExperimentalNotice } from "@/components/layout/experimental-notice";
import { useSidebarPreference } from "@/components/layout/sidebar-preference";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { OrganizationSwitcher, SignedInAs, useIsAdmin } from "@/features/auth";
import { useActivityAvailability } from "@/features/activity";
import { useAlerts } from "@/features/alerts";
import { useFeature } from "@/features/flags";
import { useSupportAvailability } from "@/features/support";
import { TeamSelector } from "@/features/teams";
import { shortenedProductName } from "@/lib/product";
import { cn } from "@/lib/utils";
import { assetUrl } from "@/lib/base-path";
import { buttonVariants } from "@/components/ui/button";

type NavItem = {
  to: string;
  label: string;
  icon: LucideIcon;
};

const environmentsItem: NavItem = { to: "/environments", label: "Environments", icon: Server };
const alertsItem: NavItem = { to: "/alerts", label: "Alerts", icon: Bell };
const activityItem: NavItem = { to: "/activity", label: "Activity", icon: ScrollText };
const supportItem: NavItem = { to: "/support", label: "Support", icon: LifeBuoy };
const agentsItem: NavItem = { to: "/agents", label: "AI agents", icon: Bot };
const teamsItem: NavItem = { to: "/teams", label: "Teams", icon: Users };
const usersItem: NavItem = { to: "/users", label: "Users", icon: UserPlus };
const settingsItem: NavItem = { to: "/settings", label: "Settings", icon: Settings };

const organizationItems = [teamsItem, usersItem, settingsItem];

const navClassName = ({ isActive }: { isActive: boolean }) =>
  cn(
    "group relative flex items-center gap-2 rounded-md px-2.5 py-2 text-sm font-medium",
    isActive
      ? "bg-accent text-foreground"
      : "text-muted-foreground hover:bg-accent hover:text-foreground",
  );

const tooltipClassName =
  "pointer-events-none absolute top-1/2 left-full z-50 ml-2 hidden -translate-y-1/2 rounded-md border border-border bg-popover px-2 py-1 text-xs whitespace-nowrap text-popover-foreground group-hover:block group-focus-within:block";

export function Sidebar({
  restoreFocusRef,
}: {
  restoreFocusRef?: RefObject<HTMLButtonElement | null>;
}) {
  // An environment with no ServiceNow Secret has no tickets to list, so the entry is
  // left out rather than leading to a page that only says so.
  const support = useSupportAvailability();
  // An environment built without the activity ring returns 501, so the entry is left
  // out rather than navigating to a page that cannot load.
  const activity = useActivityAvailability();
  // A surface that is turned off has no page to reach, so it has no entry.
  // The count is the firing criticals the control plane reports. While the read
  // is pending or has failed there is no number, and no number is not zero.
  const criticals = useAlerts().data?.criticals ?? 0;
  const environmentsOn = useFeature("clusters");
  const activityOn = useFeature("activity");
  const supportOn = useFeature("support");
  const mcpOn = useFeature("mcp");
  const admin = useIsAdmin();
  const { rail, phone, closeDrawer } = useSidebarPreference();
  const sidebarRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);

  const items = [
    ...(environmentsOn ? [environmentsItem] : []),
    alertsItem,
    ...(mcpOn ? [agentsItem] : []),
    ...(activityOn && activity.connected && !activity.checking ? [activityItem] : []),
    ...(supportOn && support.connected && !support.checking ? [supportItem] : []),
  ];

  useEffect(() => {
    if (!phone) {
      return;
    }

    closeRef.current?.focus();
    const root = sidebarRef.current;

    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        if (event.defaultPrevented) {
          return;
        }
        event.preventDefault();
        closeDrawer();
        restoreFocusRef?.current?.focus();

        return;
      }
      if (event.key !== "Tab" || !root) {
        return;
      }

      const focusable = [...root.querySelectorAll<HTMLElement>("a[href], button:not([disabled])")];
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!first || !last) {
        return;
      }
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onKeyDown);

    return () => document.removeEventListener("keydown", onKeyDown);
  }, [phone, closeDrawer, restoreFocusRef]);

  function leave() {
    if (phone) {
      closeDrawer();
    }
  }

  return (
    <div
      ref={sidebarRef}
      id="app-sidebar"
      data-testid="sidebar"
      className={cn(
        "fixed inset-y-0 left-0 z-50 flex flex-col border-r border-border bg-card",
        rail ? "w-16" : "w-64",
      )}
    >
      <SidebarHeader rail={rail} phone={phone} closeRef={closeRef} onClose={closeDrawer} />

      <div className={cn(rail ? "px-1" : "px-2 pb-2")}>
        <OrganizationSwitcher compact={rail} />
        <TeamSelector compact={rail} />
      </div>

      <nav aria-label="Main" className="flex-1 space-y-1 overflow-y-auto px-2 py-2">
        {items.map((item) => (
          <NavEntry
            key={item.to}
            item={item}
            rail={rail}
            onNavigate={leave}
            badge={
              item.to === alertsItem.to && criticals > 0 ? (
                <CriticalCount count={criticals} rail={rail} />
              ) : null
            }
          />
        ))}
      </nav>

      {admin ? (
        <div className="px-2 pb-2">
          <OrganizationNav rail={rail} onNavigate={leave} />
        </div>
      ) : null}

      <SignedInAs compact={rail} />
    </div>
  );
}

function SidebarHeader({
  rail,
  phone,
  closeRef,
  onClose,
}: {
  rail: boolean;
  phone: boolean;
  closeRef: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
}) {
  const { toggleCollapsed } = useSidebarPreference();

  const collapse = phone ? null : (
    <button
      type="button"
      className={cn(buttonVariants({ variant: "ghost", size: "icon" }), "size-7")}
      aria-expanded={!rail}
      aria-label={rail ? "Expand sidebar" : "Collapse sidebar"}
      onClick={toggleCollapsed}
    >
      {rail ? (
        <PanelLeftOpen strokeWidth={1.5} aria-hidden="true" />
      ) : (
        <PanelLeftClose strokeWidth={1.5} aria-hidden="true" />
      )}
    </button>
  );

  return (
    <div
      className={cn(
        "flex gap-1 px-2 py-2",
        rail ? "flex-col items-center" : "min-h-14 items-center justify-between px-3",
      )}
    >
      {phone ? (
        <button
          ref={closeRef}
          type="button"
          className={buttonVariants({ variant: "ghost", size: "icon" })}
          aria-label="Close menu"
          onClick={onClose}
        >
          <X strokeWidth={1.5} aria-hidden="true" />
        </button>
      ) : null}
      <ProductHome rail={rail} />
      {rail || phone ? (
        <ThemeToggle className="size-7" />
      ) : (
        <div className="flex shrink-0 items-center">
          {collapse}
          <ThemeToggle className="-mr-1 size-7 shrink-0" />
        </div>
      )}
      {rail ? collapse : null}
    </div>
  );
}

function ProductHome({ rail }: { rail: boolean }) {
  return (
    <div className={cn("flex min-w-0 gap-1.5", rail ? "flex-col items-center" : "items-start")}>
      <Link to="/" className="group relative flex min-w-0 items-center gap-1.5 rounded-md">
        <img src={assetUrl("percona-mark.svg")} alt="" className="size-5 shrink-0" />
        <span
          className={cn(
            "whitespace-nowrap text-sm font-semibold leading-tight tracking-tight",
            rail && "sr-only",
          )}
        >
          {shortenedProductName}
        </span>
        {rail ? (
          <span role="tooltip" aria-hidden="true" className={tooltipClassName}>
            {shortenedProductName}
          </span>
        ) : null}
      </Link>
      <ExperimentalNotice compact={rail} />
    </div>
  );
}

function NavEntry({
  item,
  rail,
  badge,
  onNavigate,
}: {
  item: NavItem;
  rail: boolean;
  badge: ReactNode;
  onNavigate: () => void;
}) {
  const Icon = item.icon;

  return (
    <NavLink
      to={item.to}
      onClick={onNavigate}
      className={({ isActive }) => cn(navClassName({ isActive }), rail && "justify-center px-0")}
    >
      <Icon className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
      <span className={rail ? "sr-only" : undefined}>{item.label}</span>
      {badge}
      {rail ? (
        <span role="tooltip" aria-hidden="true" className={tooltipClassName}>
          {item.label}
        </span>
      ) : null}
    </NavLink>
  );
}

function OrganizationNav({ rail, onNavigate }: { rail: boolean; onNavigate: () => void }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open || !rail) {
      return;
    }

    function onPointerDown(event: MouseEvent) {
      if (!rootRef.current?.contains(event.target as Node)) {
        setOpen(false);
      }
    }

    document.addEventListener("mousedown", onPointerDown);

    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open, rail]);

  return (
    <div ref={rootRef} className={cn("relative", rail ? "pt-2" : "pt-4")}>
      <button
        type="button"
        className={cn(navClassName({ isActive: false }), "w-full", rail && "justify-center px-0")}
        aria-expanded={open}
        aria-controls="sidebar-organization"
        onClick={() => setOpen((current) => !current)}
      >
        <Building2 className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        <span className={cn(rail ? "sr-only" : "flex-1 text-left")}>Organization</span>
        {rail ? null : open ? (
          <ChevronDown className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        ) : (
          <ChevronRight className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
        )}
        {rail && !open ? (
          <span role="tooltip" aria-hidden="true" className={tooltipClassName}>
            Organization
          </span>
        ) : null}
      </button>
      {open ? (
        <div
          id="sidebar-organization"
          role="group"
          aria-label="Organization"
          className={cn(
            rail
              ? "absolute bottom-0 left-full z-50 ml-2 w-48 rounded-lg border border-border bg-popover p-1 shadow-lg"
              : "space-y-1 pt-1",
          )}
          onKeyDown={(event) => {
            if (event.key !== "Escape") {
              return;
            }
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
          }}
        >
          {organizationItems.map((item) => (
            <NavEntry
              key={item.to}
              item={item}
              rail={false}
              badge={null}
              onNavigate={() => {
                setOpen(false);
                onNavigate();
              }}
            />
          ))}
        </div>
      ) : null}
    </div>
  );
}

function CriticalCount({ count, rail }: { count: number; rail: boolean }) {
  const words = count === 1 ? "1 critical alert firing" : `${count} critical alerts firing`;

  if (rail) {
    return (
      <span className="absolute -top-1 -right-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-xs font-medium text-destructive-foreground">
        <span className="font-mono" aria-hidden="true">
          {count}
        </span>
        <span className="sr-only">{words}</span>
      </span>
    );
  }

  return (
    <span className="ml-auto flex items-center gap-1 text-xs font-medium text-destructive">
      <CircleAlert className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      <span className="font-mono" aria-hidden="true">
        {count}
      </span>
      <span className="sr-only">{words}</span>
    </span>
  );
}
