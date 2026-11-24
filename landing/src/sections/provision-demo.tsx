import { ChevronLeft, ChevronRight, Database, Plus } from "lucide-react";
import { Bar, Button, Check, Stage, States, Window, type Act } from "@/vendor/stageshow";
import { usePageTheme } from "@/lib/page-theme";
import { engineNames, environmentName, type Selection } from "@/lib/configurator";
import { ConsoleSidebar, OutlineBadge, Pill } from "./faux-console";

type Scene = { step: number };

// The steps and their words are the console's provisioning screen.
const steps = ["Creating volumes", "Starting database", "Creating app user"];

// A database is created: each step finishes in turn, then the console lands on
// the databases list with the row Ready. The result stays on screen.
async function script(a: Act<Scene>) {
  await a.wait(900);
  await a.set({ step: 1 });
  await a.wait(1100);
  await a.set({ step: 2 });
  await a.wait(1100);
  await a.set({ step: 3 });
  await a.wait(600);
}

function stepState(step: number, index: number) {
  if (step > index) return "Done";
  return step === index ? "In progress" : "Waiting";
}

/**
 * The faux console for the chosen engine and profile: the provisioning screen,
 * then the databases list. It is decoration: the caller labels it, and nothing
 * inside it takes focus. The parent keys it on the selection, so each choice
 * plays the creation again.
 */
export function ProvisionDemo({ engine, profile }: Selection) {
  const theme = usePageTheme();

  return (
    <div aria-hidden="true" inert className="overflow-hidden">
      <Stage<Scene>
        width={600}
        height={360}
        compact={{ below: 520, width: 358, height: 340 }}
        initial={{ step: 0 }}
        poster={{ step: 3 }}
        script={script}
        play="once"
        fill
        theme={theme}
        className="fx pds-stage"
      >
        {(s, _rect, layout) => {
          const ready = s.step >= 3;
          const urls = {
            database: (
              <span className="fx-url">{`pds.percona.com/app/databases/${environmentName}/pds.${engine.database}`}</span>
            ),
            list: (
              <span className="fx-url">{`pds.percona.com/app/environments/${environmentName}`}</span>
            ),
          };

          return (
            <Window
              className="fx-window fx-hero-window"
              url={<States value={ready ? "list" : "database"} states={urls} />}
              sidebar={layout === "wide" ? <ConsoleSidebar /> : undefined}
              sidebarWidth={150}
              body={{ className: "fx-body" }}
            >
              <States
                align="start"
                value={ready ? "list" : "creating"}
                states={{
                  creating: (
                    <div className="fx-center">
                      <img src="/percona-mark.svg" alt="" className="fx-mark" />
                      <div className="fx-stack" style={{ alignItems: "center" }}>
                        <p className="fx-title">
                          Creating{" "}
                          <span className="fx-mono" style={{ fontSize: 15 }}>
                            {engine.database}
                          </span>
                        </p>
                        <p className="fx-small">Usually 6 to 12 minutes</p>
                      </div>
                      <div className="fx-card">
                        {steps.map((label, index) => {
                          const state = stepState(s.step, index);
                          return (
                            <div key={label} className="fx-step">
                              <Check on={s.step > index} pending={s.step === index} size={14} />
                              <span className="fx-grow">{label}</span>
                              <span className="fx-step-state">{state}</span>
                              <ChevronRight className="fx-icon" size={14} strokeWidth={1.5} />
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  ),
                  list: (
                    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
                      <p className="fx-engine fx-back">
                        <ChevronLeft size={12} strokeWidth={1.5} />
                        Back to environments
                      </p>
                      <div className="fx-head">
                        <div>
                          <p className="fx-title">Databases</p>
                          <p className="fx-small">
                            {engineNames("or")} on{" "}
                            <span style={{ whiteSpace: "nowrap" }}>{environmentName}</span>.
                          </p>
                        </div>
                        <Button
                          variant="primary"
                          size="small"
                          icon={<Plus size={12} strokeWidth={1.5} />}
                        >
                          <span>Create database</span>
                        </Button>
                      </div>
                      <div className="fx-card">
                        <div className="fx-db">
                          <div className="fx-stack">
                            <p className="fx-mono" style={{ fontSize: 12, fontWeight: 500 }}>
                              {engine.database}
                            </p>
                            <span className="fx-engine">
                              <Database size={12} strokeWidth={1.5} />
                              {engine.label}
                            </span>
                          </div>
                          <div className="fx-pills">
                            <Pill kind="ready">Ready</Pill>
                            <OutlineBadge>{profile.label}</OutlineBadge>
                          </div>
                          <div className="fx-backup">
                            <span>Last backup</span>
                            <span>No backups yet</span>
                          </div>
                        </div>
                        {[0, 1].map((row) => (
                          <div
                            key={row}
                            className="fx-db"
                            style={{ borderTop: "1px solid var(--ss-line)" }}
                          >
                            <div className="fx-stack" style={{ gap: 7 }}>
                              <Bar w={[96, 80][row]!} h={8} r={4} tone={2} />
                              <Bar w={[56, 64][row]!} h={7} r={3.5} tone={1} />
                            </div>
                            <Bar w={72} h={20} r={10} tone={2} />
                            <Bar w={64} h={8} r={4} tone={1} />
                          </div>
                        ))}
                      </div>
                    </div>
                  ),
                }}
              />
            </Window>
          );
        }}
      </Stage>
    </div>
  );
}
