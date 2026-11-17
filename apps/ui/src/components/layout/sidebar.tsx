import {
  Bell,
  Bot,
  CircleAlert,
  LifeBuoy,
  MessageSquare,
  ScrollText,
  Server,
  Settings,
  UserPlus,
  Users,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { NavLink } from "react-router";
import { ExperimentalNotice } from "@/components/layout/experimental-notice";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { OrganizationSwitcher, SignedInAs, useIsAdmin } from "@/features/auth";
import { useActivityAvailability } from "@/features/activity";
import { useAlerts } from "@/features/alerts";
import { useFeature } from "@/features/flags";
import { useSupportAvailability } from "@/features/support";
import { TeamSelector } from "@/features/teams";
import { productName } from "@/lib/product";
import { cn } from "@/lib/utils";
import { assetUrl } from "@/lib/base-path";

type NavItem = {
  to: string;
  label: string;
  icon: LucideIcon;
};

const environmentsItem: NavItem = { to: "/environments", label: "Environments", icon: Server };
const alertsItem: NavItem = { to: "/alerts", label: "Alerts", icon: Bell };
const activityItem: NavItem = { to: "/activity", label: "Activity", icon: ScrollText };
const supportItem: NavItem = { to: "/support", label: "Support", icon: LifeBuoy };
const chatsItem: NavItem = { to: "/chat", label: "Chats", icon: MessageSquare };
const agentsItem: NavItem = { to: "/agents", label: "AI agents", icon: Bot };
const teamsItem: NavItem = { to: "/teams", label: "Teams", icon: Users };
const usersItem: NavItem = { to: "/users", label: "Users", icon: UserPlus };

const settingsItem: NavItem = { to: "/settings", label: "Settings", icon: Settings };

const navClassName = ({ isActive }: { isActive: boolean }) =>
  cn(
    "flex items-center gap-2 rounded-md px-2.5 py-2 text-sm font-medium",
    isActive
      ? "bg-accent text-foreground"
      : "text-muted-foreground hover:bg-accent hover:text-foreground",
  );

export function Sidebar() {
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
  const chatOn = useFeature("chat");
  const mcpOn = useFeature("mcp");
  const admin = useIsAdmin();

  const items = [
    ...(environmentsOn ? [environmentsItem] : []),
    alertsItem,
    ...(chatOn ? [chatsItem] : []),
    ...(mcpOn ? [agentsItem] : []),
    ...(activityOn && activity.connected && !activity.checking ? [activityItem] : []),
    ...(supportOn && support.connected && !support.checking ? [supportItem] : []),
  ];

  return (
    <div
      data-testid="sidebar"
      className="fixed inset-y-0 left-0 flex w-64 flex-col border-r border-border bg-card"
    >
      <div className="flex h-14 items-center justify-between px-3">
        <span className="flex items-center gap-1.5">
          <img src={assetUrl("percona-mark.svg")} alt="" className="size-5 shrink-0" />
          <span className="flex flex-col items-start gap-0.5">
            <span className="whitespace-nowrap text-sm font-semibold leading-tight tracking-tight">
              {productName}
            </span>
            <ExperimentalNotice />
          </span>
        </span>
        {/* The product name leaves 28px beside the icon, so the toggle is smaller than the default and tucks into the padding. */}
        <ThemeToggle className="-mr-1 size-7 shrink-0" />
      </div>

      <div className="px-2 pb-2">
        <OrganizationSwitcher />
        <TeamSelector />
      </div>

      <nav aria-label="Main" className="flex-1 space-y-1 px-2 py-2">
        {items.map(({ to, label, icon: Icon }) => (
          <NavLink key={to} to={to} className={navClassName}>
            <Icon className="size-4" strokeWidth={1.5} aria-hidden="true" />
            {label}
            {to === alertsItem.to && criticals > 0 ? <CriticalCount count={criticals} /> : null}
          </NavLink>
        ))}

        {admin ? (
          <div role="group" aria-labelledby="sidebar-organization" className="space-y-1 pt-4">
            <p
              id="sidebar-organization"
              className="px-2.5 pb-1 text-xs font-medium text-muted-foreground"
            >
              Organization
            </p>
            {[teamsItem, usersItem, settingsItem].map((item) => (
              <NavLink key={item.to} to={item.to} className={navClassName}>
                <item.icon className="size-4" strokeWidth={1.5} aria-hidden="true" />
                {item.label}
              </NavLink>
            ))}
          </div>
        ) : null}
      </nav>

      <SignedInAs />
    </div>
  );
}

function CriticalCount({ count }: { count: number }) {
  return (
    <span className="ml-auto flex items-center gap-1 text-xs font-medium text-destructive">
      <CircleAlert className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
      <span className="font-mono" aria-hidden="true">
        {count}
      </span>
      <span className="sr-only">
        {count === 1 ? "1 critical alert firing" : `${count} critical alerts firing`}
      </span>
    </span>
  );
}
