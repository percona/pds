import { describe, expect, it } from "vitest";
import { isFirstLine, isLastLine, noHistoryCursor, recallNext, recallPrevious } from "./history";

const entries = ["scale orders-dev", "why is cpu high", "enable daily backups"];

describe("isFirstLine", () => {
  it("is true for a caret with no newline behind it", () => {
    expect(isFirstLine("", 0)).toBe(true);
    expect(isFirstLine("why is cpu high", 4)).toBe(true);
    expect(isFirstLine("why is cpu high", 15)).toBe(true);
  });

  it("is false once the caret sits on a later line", () => {
    expect(isFirstLine("first\nsecond", 6)).toBe(false);
    expect(isFirstLine("first\nsecond", 12)).toBe(false);
  });
});

describe("isLastLine", () => {
  it("is true for a caret with no newline ahead of it", () => {
    expect(isLastLine("", 0)).toBe(true);
    expect(isLastLine("first\nsecond", 6)).toBe(true);
    expect(isLastLine("first\nsecond", 12)).toBe(true);
  });

  it("is false while a later line is still ahead", () => {
    expect(isLastLine("first\nsecond", 0)).toBe(false);
    expect(isLastLine("first\nsecond", 5)).toBe(false);
  });
});

describe("recallPrevious", () => {
  it("loads the newest message and stashes the draft in progress", () => {
    const result = recallPrevious(entries, noHistoryCursor, "half typed");

    expect(result).toEqual({
      cursor: { index: 2, stash: "half typed" },
      draft: "enable daily backups",
    });
  });

  it("walks further back on each press", () => {
    const first = recallPrevious(entries, noHistoryCursor, "")!;
    const second = recallPrevious(entries, first.cursor, first.draft)!;

    expect(second.draft).toBe("why is cpu high");
    expect(recallPrevious(entries, second.cursor, second.draft)!.draft).toBe("scale orders-dev");
  });

  it("stops at the oldest message rather than clearing the box", () => {
    const oldest = { index: 0, stash: "" };

    expect(recallPrevious(entries, oldest, "scale orders-dev")).toBeUndefined();
  });

  it("does nothing when nothing has been sent yet", () => {
    expect(recallPrevious([], noHistoryCursor, "half typed")).toBeUndefined();
  });

  it("keeps the original stash while walking back, so an edit is not saved over it", () => {
    const first = recallPrevious(entries, noHistoryCursor, "half typed")!;
    const second = recallPrevious(entries, first.cursor, "edited recall")!;

    expect(second.cursor.stash).toBe("half typed");
  });
});

describe("recallNext", () => {
  it("walks forward toward the newest message", () => {
    const result = recallNext(entries, { index: 0, stash: "half typed" });

    expect(result).toEqual({ cursor: { index: 1, stash: "half typed" }, draft: "why is cpu high" });
  });

  it("restores the stashed draft when it walks past the newest message", () => {
    const result = recallNext(entries, { index: 2, stash: "half typed" });

    expect(result).toEqual({ cursor: noHistoryCursor, draft: "half typed" });
  });

  it("does nothing when the box is not showing history", () => {
    expect(recallNext(entries, noHistoryCursor)).toBeUndefined();
  });
});
