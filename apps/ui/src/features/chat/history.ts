/**
 * Recalling a sent message with the arrow keys, the way a shell or Claude Code
 * does it. The rules live here as plain functions so the composer only has to
 * decide when to ask, not what the answer is.
 */

/** Where the message box sits in the history, and the draft it interrupted. */
export type HistoryCursor = {
  /** Index into the sent messages, or null when the box shows a live draft. */
  index: number | null;
  /** The draft that was in the box when the recall started. */
  stash: string;
};

/** The box is showing a live draft, not a recalled message. */
export const noHistoryCursor: HistoryCursor = { index: null, stash: "" };

/** A recall that moved: the new cursor and what the box should show. */
export type Recall = { cursor: HistoryCursor; draft: string };

/**
 * True when the caret has no newline behind it. Arrow up only recalls from the
 * first line, so a caret deeper in a multi-line message still moves the caret.
 */
export function isFirstLine(value: string, caret: number): boolean {
  return !value.slice(0, caret).includes("\n");
}

/**
 * True when the caret has no newline ahead of it. Arrow down only walks forward
 * through history from the last line, so it still moves the caret before that.
 */
export function isLastLine(value: string, caret: number): boolean {
  return !value.slice(caret).includes("\n");
}

/**
 * One step back through the sent messages. Undefined means nothing moved,
 * either because nothing has been sent or because the oldest message is
 * already in the box, which is where the walk stops.
 */
export function recallPrevious(
  entries: string[],
  cursor: HistoryCursor,
  draft: string,
): Recall | undefined {
  if (entries.length === 0) {
    return undefined;
  }

  if (cursor.index === null) {
    const newest = entries.length - 1;

    return { cursor: { index: newest, stash: draft }, draft: entries[newest]! };
  }

  if (cursor.index === 0) {
    return undefined;
  }

  const next = cursor.index - 1;

  // The stash keeps the draft the recall interrupted, never an edited recall.
  return { cursor: { index: next, stash: cursor.stash }, draft: entries[next]! };
}

/**
 * One step forward through the sent messages. Stepping past the newest one puts
 * the interrupted draft back. Undefined means the box was not showing history.
 */
export function recallNext(entries: string[], cursor: HistoryCursor): Recall | undefined {
  if (cursor.index === null) {
    return undefined;
  }

  const next = cursor.index + 1;

  if (next > entries.length - 1) {
    return { cursor: noHistoryCursor, draft: cursor.stash };
  }

  return { cursor: { index: next, stash: cursor.stash }, draft: entries[next]! };
}
