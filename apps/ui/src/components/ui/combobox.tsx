import { Check, ChevronDown, Search } from "lucide-react";
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cn } from "@/lib/utils";

export type ComboboxOption = { value: string; label: string };

/** Gap between the trigger and the panel, and the margin kept to the viewport. */
const gap = 4;
const edge = 8;
/** The panel is never taller than this, however much room the viewport has. */
const maxPanelHeight = 320;
/** Below this the panel is too short to be worth opening downward. */
const minPanelHeight = 160;
/** A model id is longer than the trigger, so the panel may outgrow it. */
const minPanelWidth = 224;

type TriggerRect = { top: number; bottom: number; left: number; width: number };

export type PanelPlacement = {
  direction: "up" | "down";
  top: number;
  left: number;
  width: number;
  maxHeight: number;
};

/**
 * Where the panel goes. It opens downward when there is room, flips up when the
 * trigger sits near the bottom, and is always bounded by the space it has, so a
 * list of two hundred models can never grow past the window.
 */
export function panelPlacement(
  rect: TriggerRect,
  viewportHeight: number,
  viewportWidth = viewportHeight,
): PanelPlacement {
  const below = viewportHeight - rect.bottom - gap - edge;
  const above = rect.top - gap - edge;
  const down = below >= minPanelHeight || below >= above;
  const room = Math.max(down ? below : above, 0);
  const maxHeight = Math.max(Math.min(maxPanelHeight, room), 0);

  const width = Math.min(
    Math.max(rect.width, minPanelWidth),
    Math.max(viewportWidth - edge * 2, 0),
  );
  const left = Math.min(Math.max(rect.left, edge), Math.max(viewportWidth - width - edge, edge));

  return down
    ? { direction: "down", top: rect.bottom + gap, left, width, maxHeight }
    : { direction: "up", top: Math.max(rect.top - gap - maxHeight, edge), left, width, maxHeight };
}

function matches(option: ComboboxOption, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (needle === "") {
    return true;
  }

  return option.label.toLowerCase().includes(needle) || option.value.toLowerCase().includes(needle);
}

type ComboboxProps = {
  id?: string;
  ariaLabel?: string;
  options: ComboboxOption[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  placeholder?: string;
  searchLabel?: string;
  searchPlaceholder?: string;
  emptyText?: string;
  className?: string;
};

/**
 * A single-select list with a search box. Radix Select covers the short lists in
 * this app, but a provider can answer with hundreds of models, which needs both
 * a filter and a hard ceiling on the height of the list.
 *
 * The panel is portalled to the body: the chat widget clips its own overflow, so
 * a panel positioned inside it would be cut off.
 */
export function Combobox({
  id,
  ariaLabel,
  options,
  value,
  onChange,
  disabled = false,
  placeholder = "Choose one",
  searchLabel = "Search",
  searchPlaceholder = "Search",
  emptyText = "Nothing matches that search.",
  className,
}: ComboboxProps) {
  const listId = useId();
  const optionId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const activeRef = useRef<HTMLLIElement>(null);

  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const [placement, setPlacement] = useState<PanelPlacement | undefined>(undefined);

  const selected = options.find((option) => option.value === value);
  const visible = useMemo(
    () => options.filter((option) => matches(option, query)),
    [options, query],
  );

  const measure = useCallback(() => {
    const rect = triggerRef.current?.getBoundingClientRect();
    if (!rect) {
      return;
    }

    setPlacement(
      panelPlacement(
        { top: rect.top, bottom: rect.bottom, left: rect.left, width: rect.width },
        window.innerHeight,
        window.innerWidth,
      ),
    );
  }, []);

  const close = useCallback((focusTrigger = true) => {
    setOpen(false);
    setQuery("");
    if (focusTrigger) {
      triggerRef.current?.focus();
    }
  }, []);

  useLayoutEffect(() => {
    if (!open) {
      return;
    }

    measure();
    searchRef.current?.focus();
  }, [open, measure]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const onScroll = () => measure();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onScroll);

    const onPointerDown = (event: MouseEvent) => {
      const target = event.target as Node;
      if (panelRef.current?.contains(target) || triggerRef.current?.contains(target)) {
        return;
      }

      close(false);
    };

    document.addEventListener("mousedown", onPointerDown);

    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onScroll);
      document.removeEventListener("mousedown", onPointerDown);
    };
  }, [open, measure, close]);

  // The keyboard walks the filtered list, so a new filter puts it back on top.
  useEffect(() => {
    setActive(0);
  }, [query]);

  useEffect(() => {
    activeRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [active, open]);

  const openPanel = () => {
    if (disabled) {
      return;
    }

    const index = options.findIndex((option) => option.value === value);
    setActive(index === -1 ? 0 : index);
    setOpen(true);
  };

  const pick = (option: ComboboxOption) => {
    onChange(option.value);
    close();
  };

  const onSearchKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActive((current) => Math.min(current + 1, Math.max(visible.length - 1, 0)));
      return;
    }

    if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive((current) => Math.max(current - 1, 0));
      return;
    }

    if (event.key === "Home") {
      event.preventDefault();
      setActive(0);
      return;
    }

    if (event.key === "End") {
      event.preventDefault();
      setActive(Math.max(visible.length - 1, 0));
      return;
    }

    if (event.key === "Enter") {
      event.preventDefault();
      const option = visible[active];
      if (option) {
        pick(option);
      }
      return;
    }

    if (event.key === "Escape" || event.key === "Tab") {
      event.preventDefault();
      close();
    }
  };

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        id={id}
        role="combobox"
        aria-label={ariaLabel}
        aria-expanded={open}
        aria-controls={listId}
        aria-haspopup="listbox"
        disabled={disabled}
        onClick={() => (open ? close() : openPanel())}
        className={cn(
          "flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-background px-3 text-sm",
          "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary disabled:opacity-50",
          className,
        )}
      >
        <span className={cn("truncate text-left", selected ? undefined : "text-muted-foreground")}>
          {selected?.label ?? placeholder}
        </span>
        <ChevronDown
          className="size-4 shrink-0 text-muted-foreground"
          strokeWidth={1.5}
          aria-hidden="true"
        />
      </button>

      {open
        ? createPortal(
            <div
              ref={panelRef}
              data-testid="combobox-panel"
              className="fixed z-50 flex flex-col overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-md"
              style={{
                top: placement?.top ?? 0,
                left: placement?.left ?? 0,
                width: placement?.width ?? minPanelWidth,
                maxHeight: placement?.maxHeight ?? minPanelHeight,
              }}
            >
              <div className="flex shrink-0 items-center gap-2 border-b border-border px-2">
                <Search
                  className="size-4 shrink-0 text-muted-foreground"
                  strokeWidth={1.5}
                  aria-hidden="true"
                />
                <input
                  ref={searchRef}
                  type="text"
                  aria-label={searchLabel}
                  aria-controls={listId}
                  aria-activedescendant={visible[active] ? `${optionId}-${active}` : undefined}
                  placeholder={searchPlaceholder}
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  onKeyDown={onSearchKeyDown}
                  className="h-9 w-full bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
                />
              </div>

              <ul
                id={listId}
                role="listbox"
                aria-label={ariaLabel}
                className="min-h-0 overflow-y-auto p-1"
              >
                {visible.map((option, index) => (
                  <li
                    key={option.value}
                    ref={index === active ? activeRef : undefined}
                    id={`${optionId}-${index}`}
                    role="option"
                    aria-selected={option.value === value}
                    onMouseEnter={() => setActive(index)}
                    onClick={() => pick(option)}
                    className={cn(
                      "flex cursor-default items-center justify-between gap-2 rounded-md px-2 py-1.5 text-sm",
                      index === active ? "bg-accent" : undefined,
                    )}
                  >
                    <span className="truncate">{option.label}</span>
                    {option.value === value ? (
                      <Check
                        className="size-4 shrink-0 text-primary"
                        strokeWidth={1.5}
                        aria-hidden="true"
                      />
                    ) : null}
                  </li>
                ))}
              </ul>

              {visible.length === 0 ? (
                <p className="px-3 py-2 text-sm text-muted-foreground">{emptyText}</p>
              ) : null}
            </div>,
            document.body,
          )
        : null}
    </>
  );
}
