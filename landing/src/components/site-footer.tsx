import { consoleUrl, githubUrl, productName } from "@/lib/site";

export function SiteFooter() {
  return (
    <footer className="border-t border-border">
      <div className="mx-auto max-w-6xl space-y-1 px-6 pt-10 text-xs text-muted-foreground">
        <p className="font-medium text-foreground">
          Percona Database Service is an experimental project.
        </p>
        <p>
          PDS is not covered by Percona support commitments,
        </p>
        <p>For now, it's not recommended for production, use with caution.</p>
      </div>
      <div className="mx-auto flex max-w-6xl flex-col gap-6 px-6 py-10 md:flex-row md:items-center md:justify-between">
        <div className="flex items-center gap-3">
          <img src="/percona-mark.svg" alt="" className="size-5" />
          <p className="text-xs text-muted-foreground">
            © {new Date().getFullYear()} Percona. {productName} is in alpha.
          </p>
        </div>
        <nav aria-label="Footer" className="flex flex-wrap gap-x-6 gap-y-2 text-xs">
          <a href={consoleUrl} className="text-muted-foreground hover:text-foreground">
            Sign in
          </a>
          <a
            href={githubUrl}
            rel="noreferrer"
            className="text-muted-foreground hover:text-foreground"
          >
            GitHub
          </a>
          <a href="https://www.percona.com" className="text-muted-foreground hover:text-foreground">
            percona.com
          </a>
        </nav>
      </div>
    </footer>
  );
}
