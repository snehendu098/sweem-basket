"use client";

import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";
import { basescanTx, displayAsset, fmtUsd, shortHash } from "@/lib/api";
import { TokenIcon } from "@/components/TokenIcon";
import { cn } from "@/lib/utils";
import type { LegResult, LegStatus, SettleResult, Step } from "@/lib/types";

/**
 * Open state plus dismissal for every popover in the app: the settings menu,
 * the wallet menu and Picker all use this one implementation. Attach `ref` to
 * the wrapping element — a click outside it, or Escape, closes.
 */
export function useDismissable() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return { open, setOpen, ref };
}

export function Spinner({ label }: { label?: string }) {
  return (
    <span className="inline-flex items-center gap-2 text-sm text-muted-foreground">
      <span className="size-3 animate-spin rounded-full border-2 border-border border-t-foreground/70" />
      {label}
    </span>
  );
}

export function ErrorBox({ message }: { message: string }) {
  return (
    <p className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-xs break-words text-destructive">
      {message}
    </p>
  );
}

/** Modest radius everywhere. The only pills are the nav Connect button and the token picker. */
export function Button({
  children,
  variant = "primary",
  className = "",
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "ghost";
}) {
  const styles = {
    primary:
      "bg-primary text-primary-foreground hover:bg-primary/90 disabled:bg-secondary disabled:text-muted-foreground",
    ghost:
      "border border-border bg-secondary/40 text-foreground hover:bg-secondary disabled:text-muted-foreground",
  }[variant];
  return (
    <button
      {...rest}
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2 text-sm font-medium transition-colors disabled:cursor-not-allowed",
        styles,
        className,
      )}
    >
      {children}
    </button>
  );
}

/** One rounded surface. Rows inside are separated by hairlines, never boxes. */
export function Panel({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "overflow-hidden rounded-2xl border border-border bg-card",
        className,
      )}
    >
      {children}
    </div>
  );
}

export const Divider = () => <div className="h-px bg-border" />;

export function Label({ children }: { children: React.ReactNode }) {
  return <span className="text-sm text-muted-foreground">{children}</span>;
}

/**
 * Replaces the native <select>: browsers paint their own chevron and a light
 * popup that has nothing to do with this theme. Renders as a dark rounded pill.
 */
export function Picker<T extends string>({
  value,
  onChange,
  options,
  placeholder = "none",
  disabled,
  ariaLabel,
  className,
}: {
  value: T | null;
  onChange: (v: T) => void;
  options: readonly { value: T; label: string; hint?: string }[];
  placeholder?: string;
  disabled?: boolean;
  ariaLabel: string;
  className?: string;
}) {
  const { open, setOpen, ref } = useDismissable();

  const current = options.find((o) => o.value === value);

  return (
    <div className={cn("relative shrink-0", className)} ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        disabled={disabled || options.length === 0}
        aria-label={ariaLabel}
        aria-expanded={open}
        className="inline-flex max-w-56 items-center gap-2 rounded-full bg-secondary/70 py-2 pl-4 pr-3 text-sm font-medium transition-colors hover:bg-secondary disabled:cursor-not-allowed disabled:text-muted-foreground"
      >
        <span className="truncate">{current?.label ?? placeholder}</span>
        <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
      </button>
      {open && (
        <ul className="absolute right-0 z-30 mt-2 max-h-72 w-60 overflow-y-auto rounded-xl border border-border bg-popover p-1 shadow-xl">
          {options.map((o) => (
            <li key={o.value}>
              <button
                type="button"
                onClick={() => {
                  onChange(o.value);
                  setOpen(false);
                }}
                className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition-colors hover:bg-secondary/70"
              >
                <span className="truncate">{o.label}</span>
                {o.hint && (
                  <span className="ml-auto tnum text-xs text-positive">
                    {o.hint}
                  </span>
                )}
                {o.value === value && (
                  <Check className={cn("size-4 text-foreground", o.hint ? "" : "ml-auto")} />
                )}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * Hover/focus tooltip. CSS only — no state, no positioning library, and it
 * works on a control that is aria-disabled because the group is the wrapper,
 * not the control. `title` carries the same text for touch and for anything
 * that never sees :hover.
 */
export function Tooltip({
  label,
  children,
  className,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <span className={cn("group relative inline-flex", className)} title={label}>
      {children}
      <span
        role="tooltip"
        className="pointer-events-none absolute bottom-full left-1/2 z-40 mb-2 hidden -translate-x-1/2 rounded-md border border-border bg-popover px-2 py-1 text-[11px] whitespace-nowrap text-foreground shadow-xl group-hover:block group-focus-within:block"
      >
        {label}
      </span>
    </span>
  );
}

export type MultiOption<T extends string> = {
  value: T;
  label: string;
  hint?: string;
  icon?: React.ReactNode;
  /** Not selectable. `reason` is what the tooltip says about why. */
  disabled?: boolean;
  reason?: string;
};

/**
 * Many-of-N in one control, for when a row of chips would wrap into a wall.
 * Same popover mechanics as Picker — one useDismissable, one absolute list —
 * so there is only ever one popover implementation in this app.
 */
export function MultiPicker<T extends string>({
  values,
  onToggle,
  options,
  placeholder = "Select tokens",
  disabled,
  ariaLabel,
  className,
}: {
  values: readonly T[];
  onToggle: (v: T) => void;
  options: readonly MultiOption<T>[];
  placeholder?: string;
  disabled?: boolean;
  ariaLabel: string;
  className?: string;
}) {
  const { open, setOpen, ref } = useDismissable();
  const chosen = options.filter((o) => values.includes(o.value));

  return (
    <div className={cn("relative", className)} ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        disabled={disabled || options.length === 0}
        aria-label={ariaLabel}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-lg border border-border bg-secondary/40 px-3 py-2.5 text-sm transition-colors hover:bg-secondary disabled:cursor-not-allowed disabled:text-muted-foreground"
      >
        {chosen.length === 0 ? (
          <span className="text-muted-foreground">{placeholder}</span>
        ) : (
          <span className="flex min-w-0 flex-wrap items-center gap-1.5">
            {chosen.map((o) => (
              <span
                key={o.value}
                className="inline-flex items-center gap-1.5 rounded-full bg-background px-2 py-0.5 text-xs font-medium"
              >
                {o.icon}
                {o.label}
              </span>
            ))}
          </span>
        )}
        <ChevronDown className="ml-auto size-4 shrink-0 text-muted-foreground" />
      </button>
      {open && (
        <ul className="absolute left-0 right-0 z-30 mt-2 max-h-72 overflow-y-auto rounded-xl border border-border bg-popover p-1 shadow-xl">
          {options.map((o) => {
            const on = values.includes(o.value);
            const row = (
              <button
                type="button"
                aria-disabled={o.disabled}
                aria-pressed={on}
                onClick={() => !o.disabled && onToggle(o.value)}
                className={cn(
                  "flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-sm transition-colors",
                  o.disabled
                    ? "cursor-not-allowed opacity-40"
                    : "hover:bg-secondary/70",
                )}
              >
                <span
                  className={cn(
                    "grid size-4 shrink-0 place-items-center rounded border",
                    on ? "border-foreground bg-foreground" : "border-border",
                  )}
                >
                  {on && <Check className="size-3 text-background" />}
                </span>
                {o.icon}
                <span className="truncate">{o.label}</span>
                {o.hint && (
                  <span className="tnum ml-auto text-xs text-positive">{o.hint}</span>
                )}
              </button>
            );
            return (
              <li key={o.value}>
                {o.disabled && o.reason ? (
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
      )}
    </div>
  );
}

// --- execution rendering: 207, pending and null prices are first-class ---

const LEG_TONE: Record<LegStatus, string> = {
  submitted: "border-positive/30 bg-positive/10 text-positive",
  pending: "border-warning/30 bg-warning/10 text-warning",
  failed: "border-destructive/30 bg-destructive/10 text-destructive",
  skipped: "border-border bg-secondary/60 text-muted-foreground",
};

export function StatusPill({ status }: { status: string }) {
  const tone =
    LEG_TONE[status as LegStatus] ??
    (status === "confirmed"
      ? LEG_TONE.submitted
      : status === "reverted"
        ? LEG_TONE.failed
        : LEG_TONE.skipped);
  return (
    <span className={cn("rounded-md border px-2 py-0.5 text-[11px]", tone)}>
      {status}
    </span>
  );
}

function TxLink({ hash }: { hash: string }) {
  return (
    <a
      href={basescanTx(hash)}
      target="_blank"
      rel="noreferrer"
      className="tnum text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
    >
      {shortHash(hash)}
    </a>
  );
}

function Steps({ steps }: { steps: Step[] }) {
  return (
    <ol className="mt-2 space-y-1 border-l border-border pl-3">
      {steps.map((s) => (
        <li key={s.step} className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">step {s.step}</span>
          <StatusPill status={s.outcome} />
          {s.tx_hash && <TxLink hash={s.tx_hash} />}
        </li>
      ))}
    </ol>
  );
}

/**
 * Renders a deposit or withdraw response. HTTP 207 is the normal partial case:
 * show which legs moved and which did not, never one blanket failure. `pending`
 * means the receipt poll timed out — the transaction may still land, so it is
 * amber and never red, and retrying could submit the same money twice.
 */
export function SettleReport({
  result,
  status,
}: {
  result: SettleResult;
  status: number;
}) {
  const legs = result.legs ?? [];
  return (
    <div className="space-y-3">
      <div
        className={cn(
          "rounded-lg border p-3 text-sm",
          status === 207
            ? "border-warning/30 bg-warning/10 text-warning"
            : "border-positive/30 bg-positive/10 text-positive",
        )}
      >
        {status === 207
          ? `Partial — ${result.failed_legs} failed, ${result.pending_legs} pending`
          : `Settled${
              result.submitted_usd !== undefined
                ? ` · ${fmtUsd(result.submitted_usd)}`
                : ""
            }`}
      </div>
      {result.pending_legs > 0 && (
        <p className="text-xs text-warning/90">
          Pending is not failure: the receipt poll timed out. Check the hash
          before retrying — retrying can submit the same money twice.
        </p>
      )}
      {legs.length > 0 && (
        <Panel>
          <ul className="divide-y divide-border">
            {legs.map((leg, i) => (
              <LegRow key={`${leg.asset}-${i}`} leg={leg} />
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}

function LegRow({ leg }: { leg: LegResult }) {
  return (
    <li className="p-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="inline-flex items-center gap-2 text-sm font-medium">
          <TokenIcon symbol={leg.asset} size={20} />
          {displayAsset(leg.asset)}
        </span>
        <StatusPill status={leg.status} />
        <span className="tnum text-sm text-muted-foreground">
          {fmtUsd(leg.amount_usd)}
        </span>
        {leg.project && (
          <span className="text-xs text-muted-foreground">{leg.project}</span>
        )}
        {leg.tx_hash && <TxLink hash={leg.tx_hash} />}
      </div>
      {leg.reason && (
        <div className="mt-1 text-xs text-muted-foreground">{leg.reason}</div>
      )}
      {leg.steps && leg.steps.length > 0 && <Steps steps={leg.steps} />}
    </li>
  );
}
