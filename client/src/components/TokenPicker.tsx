"use client";

import { useEffect, useRef, useState } from "react";
import { Search, X } from "lucide-react";
import { fmtPct } from "@/lib/api";
import { TokenIcon } from "@/components/TokenIcon";
import { Button, Tooltip, WarningMark } from "@/components/ui";
import { Pop } from "@/components/motion";
import { cn } from "@/lib/utils";

export type PickOption = {
  id: string;
  asset: string;
  title: string;
  symbol: string;
  sub: string;
  apy: number;
  tvl: number;
  group?: string;
  disabled: boolean;
  reason: string;
  warning?: string;
};

const rowId = (id: string) => `tp-${id}`;

const selectedRing = "border-primary bg-primary/10";

export function TokenPicker({
  options,
  tiles,
  values,
  onToggle,
  disabled,
  heading,
  placeholder,
  mode,
  onModeChange,
}: {
  options: readonly PickOption[];
  tiles: readonly PickOption[];
  values: readonly string[];
  onToggle: (id: string) => void;
  disabled?: boolean;
  heading: string;
  placeholder: string;
  mode?: string;
  onModeChange?: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);

  const needle = query.trim().toLowerCase();
  const rows = needle
    ? options.filter((o) =>
        `${o.symbol} ${o.title} ${o.sub}`.toLowerCase().includes(needle),
      )
    : options;

  useEffect(() => {
    const d = dialog.current;
    if (!d) return;
    if (open && !d.open) {
      d.showModal();
      d.scrollTop = 0;
      input.current?.focus();
    } else if (!open && d.open) {
      d.close();
    }
  }, [open]);

  const idx = cursor < rows.length ? cursor : 0;

  useEffect(() => {
    if (!open) return;
    document
      .getElementById(rowId(rows[idx]?.id ?? ""))
      ?.scrollIntoView({ block: "nearest" });
  }, [idx, open, rows]);

  const chosen = options.filter((o) => values.includes(o.id));

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (rows.length === 0) return;
      const step = e.key === "ArrowDown" ? 1 : -1;
      setCursor((idx + step + rows.length) % rows.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const o = rows[idx];
      if (o && !o.disabled) onToggle(o.id);
    }
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        disabled={disabled || options.length === 0}
        aria-haspopup="dialog"
        className="flex w-full items-center gap-2 rounded-lg border border-border bg-secondary/40 px-3 py-2.5 text-sm transition-colors hover:bg-secondary disabled:cursor-not-allowed disabled:text-muted-foreground"
      >
        {chosen.length === 0 ? (
          <span className="text-muted-foreground">{placeholder}</span>
        ) : (
          <span className="flex min-w-0 flex-wrap items-center gap-1.5">
            {chosen.map((o) => (
              <span
                key={o.id}
                className="inline-flex items-center rounded-full bg-background px-2.5 py-0.5 text-xs font-medium"
              >
                {o.symbol}
              </span>
            ))}
          </span>
        )}
        <Search className="ml-auto size-4 shrink-0 text-muted-foreground" />
      </button>

      <dialog
        ref={dialog}
        onClose={() => setOpen(false)}
        onClick={(e) => e.target === dialog.current && setOpen(false)}
        onKeyDown={onKeyDown}
        aria-label={heading}
        className="m-auto w-[min(26rem,calc(100vw-2rem))] bg-transparent p-0 text-foreground backdrop:bg-black/60"
      >
        {open && (
          <Pop className="flex max-h-[min(34rem,calc(100vh-4rem))] flex-col overflow-hidden rounded-2xl border border-border bg-popover shadow-2xl">
            <div className="flex items-center justify-between px-5 pt-4">
              <h2 className="text-sm font-medium">{heading}</h2>
              <div className="flex items-center gap-1">
                {mode && onModeChange && (
                  <button
                    type="button"
                    role="switch"
                    aria-checked={mode === "manual"}
                    onClick={onModeChange}
                    className="rounded-md px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
                  >
                    {mode === "manual" ? "Manual" : "Auto"}
                    <span
                      className={`ml-2 inline-block h-1.5 w-1.5 rounded-full align-middle ${
                        mode === "manual" ? "bg-primary" : "bg-border"
                      }`}
                    />
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => setOpen(false)}
                  aria-label="Close"
                  className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                >
                  <X className="size-4" />
                </button>
              </div>
            </div>

            <div className="px-5 pt-4">
              <div className="flex items-center gap-2 rounded-xl border border-border bg-secondary/40 px-3 py-2.5 focus-within:border-foreground/30">
                <Search className="size-4 shrink-0 text-muted-foreground" />
                <input
                  ref={input}
                  value={query}
                  onChange={(e) => {
                    setQuery(e.target.value);
                    setCursor(0);
                  }}
                  placeholder="Search tokens"
                  aria-label="Search tokens"
                  role="combobox"
                  aria-expanded
                  aria-controls="tp-list"
                  aria-autocomplete="list"
                  aria-activedescendant={
                    rows[idx] ? rowId(rows[idx].id) : undefined
                  }
                  className="w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                />
              </div>
            </div>

            {tiles.length > 0 && (
              <div className="grid grid-flow-col gap-2.5 px-5 pt-4">
                {tiles.map((o) => {
                  const on = values.includes(o.id);
                  return (
                    <button
                      key={o.id}
                      type="button"
                      onClick={() => onToggle(o.id)}
                      aria-pressed={on}
                      className={cn(
                        "flex flex-col items-center gap-2 rounded-xl border px-2 py-3 transition-colors",
                        on
                          ? selectedRing
                          : "border-border hover:bg-secondary/60",
                      )}
                    >
                      <TokenIcon symbol={o.symbol} size={26} />
                      <span className="w-full truncate text-center text-[11px] font-medium">
                        {o.symbol}
                      </span>
                    </button>
                  );
                })}
              </div>
            )}

            <div className="mt-5 px-5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
              {needle
                ? `${rows.length} match${rows.length === 1 ? "" : "es"}`
                : "All tokens"}
            </div>

            <ul
              id="tp-list"
              role="listbox"
              aria-multiselectable
              aria-label={heading}
              className="mt-2 min-h-0 flex-1 space-y-1.5 overflow-y-auto px-3 pb-3"
            >
              {rows.length === 0 && (
                <li className="px-3 py-6 text-center text-sm text-muted-foreground">
                  no token matches “{query.trim()}”
                </li>
              )}
              {rows.map((o, i) => {
                const on = values.includes(o.id);
                const head =
                  o.group && o.group !== rows[i - 1]?.group ? o.group : null;
                const row = (
                  <div
                    id={rowId(o.id)}
                    role="option"
                    aria-selected={on}
                    aria-disabled={o.disabled}
                    onClick={() => !o.disabled && onToggle(o.id)}
                    onMouseEnter={() => setCursor(i)}
                    className={cn(
                      "flex w-full items-center gap-3 rounded-xl border px-3 py-3 text-left",
                      o.disabled
                        ? "cursor-not-allowed border-transparent opacity-40"
                        : "cursor-pointer",
                      !o.disabled &&
                        (on
                          ? selectedRing
                          : i === idx
                            ? "border-transparent bg-secondary/60"
                            : "border-transparent"),
                    )}
                  >
                    <TokenIcon symbol={o.symbol} size={30} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-1.5">
                        <span className="truncate text-sm font-medium">
                          {o.title}
                        </span>
                        {!o.disabled && o.warning && (
                          <WarningMark label={o.warning} />
                        )}
                      </div>
                      <div className="truncate text-xs text-muted-foreground">
                        {o.sub}
                      </div>
                    </div>
                    <span className="tnum shrink-0 text-sm text-positive">
                      {fmtPct(o.apy)}
                    </span>
                  </div>
                );
                return (
                  <li key={o.id}>
                    {head && (
                      <div className="sticky top-0 z-10 bg-popover px-3 pb-1 pt-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                        {head}
                      </div>
                    )}
                    {o.disabled ? (
                      <Tooltip label={o.reason} className="w-full">
                        {row}
                      </Tooltip>
                    ) : (
                      row
                    )}
                  </li>
                );
              })}
            </ul>

            <div className="border-t border-border p-3">
              <Button className="w-full" onClick={() => setOpen(false)}>
                {chosen.length === 0
                  ? "Done"
                  : `Done · ${chosen.length} selected`}
              </Button>
            </div>
          </Pop>
        )}
      </dialog>
    </>
  );
}
