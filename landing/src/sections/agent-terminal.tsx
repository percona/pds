import { Caret, Prompt, Stage, Stream, Window, type Act } from "@/vendor/stageshow";
import { usePageTheme } from "@/lib/page-theme";
import "./faux-console";

type Scene = { typed: number; t1: number; t2: number; words: number; clr: number; gone: boolean };

const question = "Which databases need attention?";
const answer = "orders has a High CPU warning. payments is Ready.";
const answerWords = answer.split(" ").length;

const done: Scene = {
  typed: question.length,
  t1: 2,
  t2: 2,
  words: answerWords,
  clr: 0,
  gone: false,
};

// An agent is asked a question, calls two PDS tools, and answers. The loop
// restarts because the terminal is cleared with the `clear` command.
async function script(a: Act<Scene>) {
  await a.wait(700);
  for (let i = 1; i <= question.length; i++) {
    a.put({ typed: i });
    await a.wait(38);
  }
  await a.wait(350);
  await a.set({ t1: 1 });
  await a.wait(600);
  await a.set({ t1: 2 });
  await a.wait(500);
  await a.set({ t2: 1 });
  await a.wait(600);
  await a.set({ t2: 2 });
  await a.wait(500);
  for (let i = 1; i <= answerWords; i++) {
    a.put({ words: i });
    await a.wait(110);
  }
  await a.wait(2400);
  for (let i = 1; i <= 5; i++) {
    a.put({ clr: i });
    await a.wait(110);
  }
  await a.wait(300);
  // `clear` runs: the old output leaves and the loop begins again at an empty prompt.
  await a.set({ gone: true });
  a.snap({ typed: 0, t1: 0, t2: 0, words: 0, clr: 0, gone: false });
  await a.wait(1200);
}

// The full text is laid out from the start, so typing never changes the line's width.
function Typed({ text, count }: { text: string; count: number }) {
  return (
    <>
      {text.slice(0, count)}
      <span className="fx-caret" data-on={count < text.length || undefined}>
        <Caret />
      </span>
      <span style={{ visibility: "hidden" }}>{text.slice(count)}</span>
    </>
  );
}

// A tool call and, once it returns, its short result on the same line.
function Call({
  name,
  called,
  result,
  returned,
}: {
  name: string;
  called: boolean;
  result: string;
  returned: boolean;
}) {
  return (
    <div className="fx-tline" data-on={called || undefined}>
      {"● "}
      {name}
      <span className="fx-tresult" data-on={returned || undefined}>
        {"  "}
        {result}
      </span>
    </div>
  );
}

/**
 * An agent using PDS in a terminal, with the real tool names list_databases
 * and list_alerts. It is decoration: the caller labels it, and nothing inside
 * it takes focus.
 */
export function AgentTerminal() {
  const theme = usePageTheme();

  return (
    <div aria-hidden="true" inert className="overflow-hidden">
      <Stage<Scene>
        width={520}
        height={276}
        compact={{ below: 440, width: 310, height: 286 }}
        initial={{ typed: 0, t1: 0, t2: 0, words: 0, clr: 0, gone: false }}
        poster={done}
        script={script}
        play="loop"
        theme={theme}
        className="fx pds-stage"
      >
        {(s) => (
          <Window chrome="terminal" title="Claude Code" className="fx-window fx-term-window">
            <div className="fx-tgroup" data-gone={s.gone || undefined}>
              <Prompt path="~/app">list my environments</Prompt>
              <Call name="list_environments" called result="1 environment" returned />
              <Call name="list_support_tickets" called result="no tickets" returned />
              <Prompt path="~/app">
                <Typed text={question} count={s.typed} />
              </Prompt>
              <Call
                name="list_databases"
                called={s.t1 >= 1}
                result="2 databases"
                returned={s.t1 >= 2}
              />
              <Call
                name="list_alerts"
                called={s.t2 >= 1}
                result="1 alert, warning"
                returned={s.t2 >= 2}
              />
              <p className="fx-answer">
                <Stream text={answer} count={s.words} />
              </p>
              <div className="fx-tline" data-on={s.words >= answerWords || undefined}>
                <Prompt path="~/app">
                  <Typed text="clear" count={s.clr} />
                </Prompt>
              </div>
            </div>
          </Window>
        )}
      </Stage>
    </div>
  );
}
