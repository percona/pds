import { Info, ShieldCheck, TriangleAlert } from "lucide-react";
import { useId, useState, type ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InlineError } from "@/components/ui/inline-error";
import { Label } from "@/components/ui/label";
import { NativeSelect } from "@/components/ui/native-select";
import { accessChoices, accessLabels, type AiAccess } from "@/features/organization-ai";
import { cn } from "@/lib/utils";
import { safeRedirect } from "../return-path";
import { useApproveConsent, useDenyConsent } from "../hooks/use-consent";
import type { ConsentChoice, ConsentClient, ConsentOrganization, ConsentRequest } from "../types";

/** The choices an Organization starts with: read, the person's own Team, no boxes. */
function defaults(organization: ConsentOrganization | undefined): ConsentChoice {
  const ownTeam = organization?.teams.find((team) => team.id === organization.ownTeamId);

  return {
    organizationId: organization?.id ?? 0,
    teamId: (ownTeam ?? organization?.teams[0])?.id ?? 0,
    access: "read",
    passwords: false,
    sqlWrites: false,
  };
}

/** The tiers up to the highest one offered, in the order the settings page lists them. Read is always offered. */
function offeredTiers(highest: AiAccess) {
  const last = Math.max(
    accessChoices.findIndex((candidate) => candidate.id === highest),
    0,
  );

  return accessChoices.slice(0, last + 1);
}

type Props = { request: string; consent: ConsentRequest; onDecided: (url: string) => void };

/**
 * Who is asking and what to give it. Nothing is decided on load: the person
 * answers with one of the two buttons, and each is a request of its own.
 */
export function ConsentForm({ request, consent, onDecided }: Props) {
  const id = useId();
  const approve = useApproveConsent(request);
  const deny = useDenyConsent(request);
  const [choice, setChoice] = useState<ConsentChoice>(() => defaults(consent.organizations[0]));
  const [refusal, setRefusal] = useState<string>();

  const organization =
    consent.organizations.find((candidate) => candidate.id === choice.organizationId) ??
    consent.organizations[0];
  const busy = approve.isPending || deny.isPending;

  const finish = (url: string) => {
    const target = safeRedirect(url);
    if (target === null) {
      setRefusal("The app sent you to an address this console will not open.");

      return;
    }
    onDecided(target);
  };
  const fail = (error: Error) => setRefusal(error.message);

  if (organization === undefined) {
    return (
      <InlineError message="You do not belong to an organization that can sign this app in." />
    );
  }

  return (
    <form
      className="space-y-6"
      onSubmit={(event) => {
        event.preventDefault();
        setRefusal(undefined);
        approve.mutate(choice, { onSuccess: finish, onError: fail });
      }}
    >
      <WhoIsAsking client={consent.client} />

      <section aria-labelledby={`${id}-where`} className="space-y-3">
        <h2 id={`${id}-where`} className="text-base font-semibold">
          Where it acts
        </h2>
        {consent.organizations.length > 1 ? (
          <div className="space-y-1.5">
            <Label htmlFor={`${id}-organization`} className="block">
              Organization
            </Label>
            <NativeSelect
              id={`${id}-organization`}
              className="w-full"
              value={String(organization.id)}
              onChange={(event) => {
                const next = consent.organizations.find(
                  (candidate) => String(candidate.id) === event.target.value,
                );
                if (next !== undefined) {
                  setChoice(defaults(next));
                }
              }}
            >
              {consent.organizations.map((candidate) => (
                <option key={candidate.id} value={candidate.id}>
                  {candidate.name}
                </option>
              ))}
            </NativeSelect>
          </div>
        ) : (
          <Row label="Organization">{organization.name}</Row>
        )}
        {organization.teams.length > 1 ? (
          <div className="space-y-1.5">
            <Label htmlFor={`${id}-team`} className="block">
              Team
            </Label>
            <NativeSelect
              id={`${id}-team`}
              className="w-full"
              value={String(choice.teamId)}
              onChange={(event) => setChoice({ ...choice, teamId: Number(event.target.value) })}
            >
              {organization.teams.map((team) => (
                <option key={team.id} value={team.id}>
                  {team.name}
                </option>
              ))}
            </NativeSelect>
          </div>
        ) : (
          <Row label="Team">{organization.teams[0]?.name}</Row>
        )}
      </section>

      <fieldset className="space-y-2">
        <legend id={`${id}-access`} className="text-base font-semibold">
          What it may do
        </legend>
        <div role="radiogroup" aria-labelledby={`${id}-access`} className="space-y-2">
          {offeredTiers(organization.access).map(({ id: tier, hint }) => {
            const selected = choice.access === tier;

            return (
              <div
                key={tier}
                className={cn(
                  "rounded-lg border p-3 text-sm has-[:focus-visible]:outline-2 has-[:focus-visible]:outline-offset-2 has-[:focus-visible]:outline-primary",
                  selected ? "border-primary bg-accent" : "border-border",
                )}
              >
                <label className="flex cursor-pointer items-center gap-2 font-medium">
                  <input
                    type="radio"
                    name={`${id}-access`}
                    className="size-4 accent-primary"
                    checked={selected}
                    aria-describedby={`${id}-${tier}-hint`}
                    onChange={() => setChoice({ ...choice, access: tier })}
                  />
                  {accessLabels[tier]}
                </label>
                <p id={`${id}-${tier}-hint`} className="pl-6 text-muted-foreground">
                  {hint}
                </p>
              </div>
            );
          })}
        </div>
      </fieldset>

      {organization.passwords || organization.sqlWrites ? (
        <div className="space-y-3">
          {organization.passwords ? (
            <Box
              id={`${id}-passwords`}
              label="Let local agents fetch database passwords into this computer's keychain"
              checked={choice.passwords}
              onChange={(passwords) => setChoice({ ...choice, passwords })}
            />
          ) : null}
          {organization.sqlWrites ? (
            <Box
              id={`${id}-sql`}
              label="Let local agents change data with SQL"
              checked={choice.sqlWrites}
              onChange={(sqlWrites) => setChoice({ ...choice, sqlWrites })}
            />
          ) : null}
        </div>
      ) : null}

      <InlineError message={refusal ?? approve.error?.message ?? deny.error?.message} />

      <div className="flex items-center justify-end gap-2">
        <Button
          type="button"
          variant="outline"
          disabled={busy}
          loading={deny.isPending}
          onClick={() => {
            setRefusal(undefined);
            deny.mutate(undefined, { onSuccess: finish, onError: fail });
          }}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={deny.isPending} loading={approve.isPending}>
          Allow access
        </Button>
      </div>
    </form>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right font-medium">{children}</span>
    </div>
  );
}

function Box({
  id,
  label,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-2">
      <input
        id={id}
        type="checkbox"
        className="mt-0.5 size-4 accent-primary"
        checked={checked}
        aria-describedby={`${id}-help`}
        onChange={(event) => onChange(event.target.checked)}
      />
      <div className="space-y-0.5">
        <Label htmlFor={id}>{label}</Label>
        <p id={`${id}-help`} className="text-sm text-muted-foreground">
          It stays off unless you tick it.
        </p>
      </div>
    </div>
  );
}

/**
 * What the control plane knows about the app, and what it does not. The name
 * is the app's own claim, so it is labelled as one and the address it proved
 * comes beside it: a lookalike can copy a name and cannot copy an address.
 */
function WhoIsAsking({ client }: { client: ConsentClient }) {
  const id = useId();

  return (
    <section
      aria-labelledby={id}
      className="divide-y divide-border rounded-lg border border-border px-4"
    >
      <h2 id={id} className="py-3 text-base font-semibold">
        Who is asking
      </h2>

      <div className="space-y-1.5 py-3">
        <Row label="Name">{client.name}</Row>
        {client.kind === "local" ? (
          <Status icon={<ShieldCheck strokeWidth={1.5} aria-hidden="true" />}>
            Built into this control plane.
          </Status>
        ) : (
          <Badge variant="muted">
            <Info className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
            Name chosen by the app
          </Badge>
        )}
      </div>

      {client.kind === "cimd" ? (
        <div className="space-y-1.5 py-3">
          <Row label="App address">
            <span className="font-mono">{client.host}</span>
          </Row>
          <Status icon={<ShieldCheck strokeWidth={1.5} aria-hidden="true" />}>
            The app proved it controls this address.
          </Status>
        </div>
      ) : null}
      {client.kind === "dcr" ? (
        <div className="py-3">
          <Status icon={<TriangleAlert strokeWidth={1.5} aria-hidden="true" />}>
            This app registered itself and has no address to check, so nothing here proves who it
            is.
          </Status>
        </div>
      ) : null}

      <div className="space-y-1.5 py-3">
        <Row label="Sends you back to">
          <span className="font-mono">{client.redirectHost}</span>
        </Row>
        {client.loopback ? (
          <Status icon={<TriangleAlert strokeWidth={1.5} aria-hidden="true" />}>
            This app runs on this computer. Only allow it if you just started it.
          </Status>
        ) : null}
      </div>
    </section>
  );
}

function Status({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <p className="flex items-start gap-2 text-sm text-muted-foreground [&_svg]:mt-0.5 [&_svg]:size-4 [&_svg]:shrink-0">
      {icon}
      <span>{children}</span>
    </p>
  );
}
