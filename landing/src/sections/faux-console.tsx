import {
  Bell,
  Bot,
  CircleAlert,
  CircleCheck,
  Loader,
  LifeBuoy,
  MessageSquare,
  ScrollText,
  Server,
  TriangleAlert,
  type LucideIcon,
} from "lucide-react";
import { Badge } from "@/vendor/stageshow";
import "@/stage-theme.css";
import "./faux-console.css";

// The console's sidebar entries, in its order and with its icons.
const navItems: { label: string; icon: LucideIcon }[] = [
  { label: "Environments", icon: Server },
  { label: "Alerts", icon: Bell },
  { label: "Chats", icon: MessageSquare },
  { label: "AI agents", icon: Bot },
  { label: "Activity", icon: ScrollText },
  { label: "Support", icon: LifeBuoy },
];

/** The console's sidebar with the first entry, Environments, selected. */
export function ConsoleSidebar() {
  return (
    <div className="fx-side">
      <p className="fx-brand">
        <img src="/percona-mark.svg" alt="" />
        Percona Database Service
      </p>
      {navItems.map(({ label, icon: Icon }, index) => (
        <span key={label} className="fx-nav" data-active={index === 0 || undefined}>
          <Icon size={14} strokeWidth={1.5} />
          {label}
        </span>
      ))}
    </div>
  );
}

type PillKind = "ready" | "creating" | "warning" | "critical" | "pending";

const pills: Record<
  PillKind,
  { icon: LucideIcon; tone: "success" | "neutral" | "warning" | "danger" }
> = {
  ready: { icon: CircleCheck, tone: "success" },
  creating: { icon: Loader, tone: "neutral" },
  warning: { icon: TriangleAlert, tone: "warning" },
  critical: { icon: CircleAlert, tone: "danger" },
  pending: { icon: CircleCheck, tone: "neutral" },
};

/** The console's status pill: an icon and a word, never color alone. */
export function Pill({ kind, children }: { kind: PillKind; children: string }) {
  const { icon: Icon, tone } = pills[kind];

  return (
    <Badge tone={tone} icon={<Icon size={12} strokeWidth={1.5} />}>
      {children}
    </Badge>
  );
}

/** The console's outline badge, as the profile name on a database row. */
export function OutlineBadge({ children }: { children: string }) {
  return <Badge className="fx-outline">{children}</Badge>;
}
