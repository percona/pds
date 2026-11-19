import { buttonVariants } from "@/components/ui/button";
import { consoleUrl, githubUrl, productName, sectionIds } from "@/lib/site";
import { ThemeToggle } from "./theme-toggle";

const navItems = [
  { href: `#${sectionIds.tryIt}`, label: "Try it" },
  { href: `#${sectionIds.proof}`, label: "Built on" },
  { href: `#${sectionIds.operations}`, label: "Operations" },
  { href: `#${sectionIds.pricing}`, label: "Pricing" },
  { href: `#${sectionIds.faq}`, label: "FAQ" },
];

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-40 border-b border-border bg-background">
      <div className="mx-auto flex h-16 max-w-6xl items-center gap-6 px-6">
        <a href="#top" className="flex items-center gap-2 rounded-md">
          <img src="/percona-mark.svg" alt="" className="size-6" />
          <span className="text-sm font-semibold tracking-tight">
            <span className="hidden sm:inline">{productName}</span>
            <span className="sm:hidden">PDS</span>
          </span>
        </a>

        <nav aria-label="Sections" className="hidden items-center gap-1 lg:flex">
          {navItems.map((item) => (
            <a
              key={item.href}
              href={item.href}
              className="rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:text-foreground"
            >
              {item.label}
            </a>
          ))}
        </nav>

        <div className="ml-auto flex items-center gap-1 sm:gap-2">
          <a
            href={githubUrl}
            rel="noreferrer"
            className="hidden rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:text-foreground sm:inline"
          >
            GitHub
          </a>
          <ThemeToggle />
          <a href={consoleUrl} className={buttonVariants({ size: "sm" })}>
            Sign in
          </a>
        </div>
      </div>
    </header>
  );
}
