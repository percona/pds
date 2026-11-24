import { ArrowRight, Cloud, FlaskConical, RotateCw } from "lucide-react";
import { Fragment, useState } from "react";
import { ChoiceGroup } from "@/components/choice-group";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  describeSelection,
  engineNames,
  engines,
  joinCommand,
  profiles,
  type EngineId,
  type ProfileId,
} from "@/lib/configurator";
import { consoleUrl, sectionIds } from "@/lib/site";
import { useReducedMotion } from "@/vendor/stageshow/reduced-motion";
import { ProvisionDemo } from "./provision-demo";

const engineChoices = engines.map(({ id, label, logo }) => ({ id, label, logo }));
const profileChoices = profiles.map(({ id, label, consequence }) => ({
  id,
  label,
  // A non-breaking hyphen keeps "on-demand" on one line.
  detail: consequence.replace("-", "\u2011"),
}));

export function Hero() {
  const [engineId, setEngineId] = useState<EngineId>("mysql");
  const [profileId, setProfileId] = useState<ProfileId>("prod");
  const [replays, setReplays] = useState(0);
  // The demo shows its finished frame under reduced motion, so there is nothing to replay.
  const reducedMotion = useReducedMotion();

  const engine = engines.find((item) => item.id === engineId)!;
  const profile = profiles.find((item) => item.id === profileId)!;
  const selection = { engine, profile };

  return (
    <section id="top" aria-labelledby="hero-title" className="px-6 pt-6 pb-16 sm:pt-14 lg:pb-20">
      <div className="mx-auto grid max-w-6xl gap-8 lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)] lg:gap-12">
        <div className="min-w-0 space-y-6 sm:space-y-8">
          <div className="animate-fade-in space-y-4 sm:space-y-5">
            <p className="inline-flex items-center gap-1.5 rounded-full border border-primary bg-card px-3 py-1 text-xs font-semibold tracking-wide uppercase">
              <FlaskConical
                className="size-3.5 text-primary"
                strokeWidth={1.5}
                aria-hidden="true"
              />
              Experimental
            </p>

            <h1
              id="hero-title"
              className="text-3xl font-semibold tracking-tight text-balance sm:text-5xl"
            >
              {engineNames("and")} on Amazon EKS, backed by Percona.
            </h1>

            <p className="max-w-xl text-base text-muted-foreground text-pretty sm:text-lg">
              Percona Database Service creates an Amazon EKS cluster in your AWS account and runs
              your databases there, from one command.
            </p>

            <a
              href={consoleUrl}
              className={buttonVariants({ size: "lg", className: "w-full sm:w-auto" })}
            >
              Sign in
              <ArrowRight strokeWidth={1.5} aria-hidden="true" />
            </a>
          </div>

          <div id={sectionIds.tryIt} className="space-y-3 sm:space-y-4">
            <ChoiceGroup
              legend="Engine"
              name="engine"
              choices={engineChoices}
              value={engineId}
              onChange={setEngineId}
              className="grid-cols-2"
            />
            <ChoiceGroup
              legend="Profile"
              name="profile"
              choices={profileChoices}
              value={profileId}
              onChange={setProfileId}
              className="grid-cols-3"
            />
            <p className="flex items-center gap-2 text-sm text-muted-foreground">
              <Cloud className="size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
              Runs on Amazon EKS in your AWS account
            </p>
          </div>
        </div>

        <div className="min-w-0">
          <div className="overflow-hidden rounded-lg border border-border bg-card">
            <figure className="relative bg-muted/40">
              <figcaption className="sr-only" aria-live="polite">
                {describeSelection(selection)}
              </figcaption>
              <ProvisionDemo key={`${engineId}-${profileId}-${replays}`} {...selection} />
            </figure>

            <div className="space-y-3 border-t border-border p-4 sm:p-5">
              <div className="flex items-start justify-between gap-3">
                <h2 className="text-base font-semibold">Run this command</h2>
                {reducedMotion ? null : (
                  <Button
                    variant="ghost"
                    size="icon"
                    className="-mt-2 -mr-2 shrink-0"
                    aria-label="Replay the preview"
                    onClick={() => setReplays((count) => count + 1)}
                  >
                    <RotateCw strokeWidth={1.5} aria-hidden="true" />
                  </Button>
                )}
              </div>
              <p className="text-sm text-muted-foreground">
                Needs aws, eksctl, kubectl, helm, and python3 on your machine. It asks for an AWS
                region.
              </p>
              <pre className="rounded-md bg-muted px-3 py-2 font-mono text-xs break-words whitespace-pre-wrap">
                {joinCommand.split("/").map((part, index, parts) => (
                  <Fragment key={index}>
                    {part}
                    {index < parts.length - 1 ? "/" : null}
                    <wbr />
                  </Fragment>
                ))}
              </pre>
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
