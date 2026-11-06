import { ChevronRight, CircleCheck } from "lucide-react";
import { useId, useState } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Feature } from "@/features/flags";
import {
  useClearConnection,
  useSaveAssistant,
  useSaveCursor,
  useSaveServiceNow,
} from "../hooks/use-connection-mutations";
import { useConnections } from "../hooks/use-connections";
import type { ConnectionKind, ConnectionsStatus } from "../types";

/**
 * Support, Anthropic and Cursor keys, pasted here
 * and stored by the adapter. The values never come back on GET. Support only
 * collects the client token: the API key and instance URL come from env. A
 * hub offers this on its environment list. An install that is not a hub offers
 * it on the database list. The panel carries its own heading rather than
 * borrowing the page's.
 */
export function ConnectionsPanel() {
  const { data, error, isPending } = useConnections();
  const [open, setOpen] = useState(false);
  const bodyId = useId();

  return (
    <div className="space-y-4" data-testid="connections-panel">
      <h2 className="text-base font-semibold">
        <button
          type="button"
          className="inline-flex items-center gap-1 rounded-md focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
          aria-expanded={open}
          aria-controls={open ? bodyId : undefined}
          onClick={() => setOpen((current) => !current)}
        >
          <ChevronRight
            className={open ? "size-4 rotate-90" : "size-4"}
            strokeWidth={1.5}
            aria-hidden="true"
          />
          Connections
        </button>
      </h2>
      {open ? (
        <div id={bodyId} className="space-y-4">
          {isPending ? <ConnectionsSkeleton /> : null}
          {error ? (
            <Card className="px-5 py-6" data-testid="connections-error">
              <p className="text-sm text-muted-foreground">{error.message}</p>
            </Card>
          ) : data ? (
            <ConnectionsBody data={data} />
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function ConnectionsBody({ data }: { data: ConnectionsStatus }) {
  return (
    <>
      <p className="text-sm text-muted-foreground">
        Tokens and keys are stored by the control plane. A stored value never comes back on a read.
      </p>
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <ServiceNowCard configured={data.servicenow.configured} />
        {/* Every card below holds a model key. With the assistant off there is
            nothing on this adapter that would use one. */}
        <Feature name="assistant">
          <ModelKeyCards data={data} />
        </Feature>
      </div>
    </>
  );
}

/** The model key cards Chat and the assistant share. */
export function ModelKeyCards({ data }: { data: ConnectionsStatus }) {
  return (
    <>
      <AssistantCard configured={data.assistant.configured} />
      <CursorCard configured={data.cursor.configured} />
    </>
  );
}

function ServiceNowCard({ configured }: { configured: boolean }) {
  const tokenId = useId();
  const [clientToken, setClientToken] = useState("");
  const save = useSaveServiceNow();
  const [confirmClear, setConfirmClear] = useState(false);

  const sendable = clientToken.trim() !== "";

  return (
    <Card className="space-y-4 p-5" data-testid="connection-servicenow">
      <CardHeader
        title="Support"
        detail="A client token so the control plane can open tickets. The API key and instance are configured by Percona."
        configured={configured}
      />
      {configured ? (
        <ClearButton label="Clear token" onClick={() => setConfirmClear(true)} />
      ) : (
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate(
              {
                clientToken: clientToken.trim(),
              },
              {
                onSuccess: () => toast.success("Support token saved"),
                onError: (failed) => toast.error(failed.message),
              },
            );
          }}
        >
          <Field id={tokenId} label="Client token" help="Sent in the request body.">
            {(props) => (
              <Input
                {...props}
                type="password"
                autoComplete="off"
                value={clientToken}
                onChange={(event) => setClientToken(event.target.value)}
              />
            )}
          </Field>
          {save.error ? <p className="text-sm text-destructive">{save.error.message}</p> : null}
          <Button type="submit" disabled={!sendable} loading={save.isPending}>
            {save.isPending ? "Saving..." : "Save token"}
          </Button>
        </form>
      )}
      <ClearDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        kind="servicenow"
        title="Clear support token"
        description="The control plane will not open tickets until you paste a new token."
      />
    </Card>
  );
}

function AssistantCard({ configured }: { configured: boolean }) {
  const apiKeyId = useId();
  const baseUrlId = useId();
  const [apiKey, setApiKey] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const save = useSaveAssistant();
  const [confirmClear, setConfirmClear] = useState(false);

  const sendable = apiKey.trim() !== "";

  return (
    <Card className="space-y-4 p-5" data-testid="connection-assistant">
      <CardHeader
        title="Assistant"
        detail="An Anthropic key so Chat can use Claude models."
        configured={configured}
      />
      {configured ? (
        <ClearButton label="Clear key" onClick={() => setConfirmClear(true)} />
      ) : (
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate(
              {
                apiKey: apiKey.trim(),
                baseUrl: baseUrl.trim() || undefined,
              },
              {
                onSuccess: () => toast.success("Assistant key saved"),
                onError: (failed) => toast.error(failed.message),
              },
            );
          }}
        >
          <Field
            id={apiKeyId}
            label="API key"
            help="Stored by the control plane. It never comes back on a read."
          >
            {(props) => (
              <Input
                {...props}
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(event) => setApiKey(event.target.value)}
              />
            )}
          </Field>
          <Field
            id={baseUrlId}
            label="API base URL"
            help="Optional. Leave blank for api.anthropic.com."
          >
            {(props) => (
              <Input
                {...props}
                type="url"
                autoComplete="off"
                value={baseUrl}
                onChange={(event) => setBaseUrl(event.target.value)}
              />
            )}
          </Field>
          {save.error ? <p className="text-sm text-destructive">{save.error.message}</p> : null}
          <Button type="submit" disabled={!sendable} loading={save.isPending}>
            {save.isPending ? "Saving..." : "Save key"}
          </Button>
        </form>
      )}
      <ClearDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        kind="assistant"
        title="Clear assistant key"
        description="The control plane will not use Claude in Chat until you paste a new key."
      />
    </Card>
  );
}

function CursorCard({ configured }: { configured: boolean }) {
  const apiKeyId = useId();
  const [apiKey, setApiKey] = useState("");
  const save = useSaveCursor();
  const [confirmClear, setConfirmClear] = useState(false);

  const sendable = apiKey.trim() !== "";

  return (
    <Card className="space-y-4 p-5" data-testid="connection-cursor">
      <CardHeader
        title="Cursor"
        detail="A Cursor key so Chat can use Cursor models."
        configured={configured}
      />
      {configured ? (
        <ClearButton label="Clear key" onClick={() => setConfirmClear(true)} />
      ) : (
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault();
            save.mutate(
              { apiKey: apiKey.trim() },
              {
                onSuccess: () => toast.success("Cursor key saved"),
                onError: (failed) => toast.error(failed.message),
              },
            );
          }}
        >
          <Field
            id={apiKeyId}
            label="API key"
            help="Stored by the control plane. It never comes back on a read."
          >
            {(props) => (
              <Input
                {...props}
                type="password"
                autoComplete="off"
                value={apiKey}
                onChange={(event) => setApiKey(event.target.value)}
              />
            )}
          </Field>
          {save.error ? <p className="text-sm text-destructive">{save.error.message}</p> : null}
          <Button type="submit" disabled={!sendable} loading={save.isPending}>
            {save.isPending ? "Saving..." : "Save key"}
          </Button>
        </form>
      )}
      <ClearDialog
        open={confirmClear}
        onOpenChange={setConfirmClear}
        kind="cursor"
        title="Clear Cursor key"
        description="The control plane will not use Cursor in Chat until you paste a new key."
      />
    </Card>
  );
}

function CardHeader({
  title,
  detail,
  configured,
}: {
  title: string;
  detail: string;
  configured: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="space-y-1">
        <h3 className="text-sm font-semibold">{title}</h3>
        <p className="text-sm text-muted-foreground">{detail}</p>
      </div>
      {configured ? (
        <Badge variant="success">
          <CircleCheck className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
          Connected
        </Badge>
      ) : null}
    </div>
  );
}

function ClearButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button type="button" variant="outline" onClick={onClick}>
      {label}
    </Button>
  );
}

function ClearDialog({
  open,
  onOpenChange,
  kind,
  title,
  description,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  kind: ConnectionKind;
  title: string;
  description: string;
}) {
  const clear = useClearConnection();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        {clear.error ? <p className="text-sm text-destructive">{clear.error.message}</p> : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            loading={clear.isPending}
            onClick={() => {
              clear.mutate(kind, {
                onSuccess: () => {
                  toast.success(
                    {
                      servicenow: "Support token cleared",
                      assistant: "Assistant key cleared",
                      cursor: "Cursor key cleared",
                    }[kind],
                  );
                  onOpenChange(false);
                },
              });
            }}
          >
            {clear.isPending ? "Clearing..." : "Clear"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ConnectionsSkeleton() {
  return (
    <div className="space-y-4" data-testid="connections-loading">
      <Skeleton className="h-4 w-32" />
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <Card className="space-y-3 p-5">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-9 w-full" />
        </Card>
        <Card className="space-y-3 p-5">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-9 w-full" />
        </Card>
        <Card className="space-y-3 p-5">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-9 w-full" />
        </Card>
      </div>
    </div>
  );
}
