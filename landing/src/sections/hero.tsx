import { ArrowRight, Check, FlaskConical } from "lucide-react";
import { buttonVariants } from "@/components/ui/button";
import { consoleUrl } from "@/lib/site";
import { ConsolePreview } from "./console-preview";

const facts = [
  "Amazon EKS in your AWS account, or a Kubernetes cluster you already have",
  "Backups stay in your account",
  "Open source underneath",
];

export function Hero() {
  return (
    <section id="top" aria-labelledby="hero-title" className="px-6 pt-16 pb-20 sm:pt-24">
      <div className="mx-auto max-w-3xl animate-fade-in text-center">
        <p className="inline-flex items-center gap-1.5 rounded-full border border-border bg-card px-3 py-1 text-xs font-medium">
          <FlaskConical className="size-3.5 text-primary" strokeWidth={1.5} aria-hidden="true" />
          Alpha
        </p>

        <h1
          id="hero-title"
          className="mt-6 text-4xl font-semibold tracking-tight text-balance sm:text-5xl"
        >
          Run MySQL and PostgreSQL on Amazon EKS, with Percona behind you.
        </h1>

        <p className="mx-auto mt-6 max-w-2xl text-base text-muted-foreground text-pretty sm:text-lg">
          Percona Database Service (PDS) runs MySQL and PostgreSQL on a Kubernetes cluster in your
          AWS account. One command can create an Amazon EKS cluster there, or join a Kubernetes
          cluster you already have, and your team gets one place to create, scale, back up and
          monitor databases. It is open source, so the databases keep running if you stop using PDS.
        </p>

        <div className="mt-8 flex flex-col items-center justify-center gap-3 sm:flex-row">
          <a
            href={consoleUrl}
            className={buttonVariants({ size: "lg", className: "w-full sm:w-auto" })}
          >
            Sign in
            <ArrowRight strokeWidth={1.5} aria-hidden="true" />
          </a>
        </div>

        <ul className="mt-8 flex flex-wrap items-center justify-center gap-x-6 gap-y-2 text-sm text-muted-foreground">
          {facts.map((fact) => (
            <li key={fact} className="flex items-center gap-1.5">
              <Check className="size-4 text-success" strokeWidth={1.5} aria-hidden="true" />
              {fact}
            </li>
          ))}
        </ul>
      </div>

      <div className="mt-16">
        <ConsolePreview />
      </div>
    </section>
  );
}
