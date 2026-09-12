"use client";

import { animate, motion, useReducedMotion } from "motion/react";
import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

/**
 * Every animation here is gated on prefers-reduced-motion: when the user has
 * asked for less motion the components render their final state immediately,
 * they do not render a faster version of the same movement.
 */

const EASE = [0.16, 1, 0.3, 1] as const;

/** Fade-and-rise. */
export function Reveal({
  children,
  delay = 0,
  className,
  y = 10,
}: {
  children: React.ReactNode;
  delay?: number;
  className?: string;
  y?: number;
}) {
  const reduced = useReducedMotion();
  if (reduced) return <div className={className}>{children}</div>;
  return (
    <motion.div
      className={className}
      initial={{ opacity: 0, y }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.4, delay, ease: EASE }}
    >
      {children}
    </motion.div>
  );
}

/**
 * Counts a real number up on first paint. `format` owns the rendering, so a
 * value the API could not produce is never turned into a number here — pass
 * the formatted string through some other element instead.
 */
export function CountUp({
  value,
  format,
  className,
  duration = 0.7,
}: {
  value: number;
  format: (n: number) => string;
  className?: string;
  duration?: number;
}) {
  const reduced = useReducedMotion();
  // Reduced motion renders the final value with no state and no effect at all.
  if (reduced) return <span className={className}>{format(value)}</span>;
  return (
    <Counting
      value={value}
      format={format}
      className={className}
      duration={duration}
    />
  );
}

function Counting({
  value,
  format,
  className,
  duration,
}: {
  value: number;
  format: (n: number) => string;
  className?: string;
  duration: number;
}) {
  const [shown, setShown] = useState(0);

  useEffect(() => {
    const controls = animate(0, value, {
      duration,
      ease: "easeOut",
      onUpdate: setShown,
    });
    return () => controls.stop();
  }, [value, duration]);

  return <span className={className}>{format(shown)}</span>;
}

/** Segmented tab control with a layout-animated indicator. Not a pill. */
export function Segmented<T extends string>({
  value,
  onChange,
  options,
  id,
  className,
}: {
  value: T;
  onChange: (v: T) => void;
  options: readonly { value: T; label: string }[];
  /** Distinguishes indicators when two segmented controls share a page. */
  id: string;
  className?: string;
}) {
  const reduced = useReducedMotion();
  return (
    <div
      role="tablist"
      className={cn(
        "grid w-full grid-flow-col rounded-lg border border-border bg-card p-1",
        className,
      )}
    >
      {options.map((o) => {
        const on = o.value === value;
        return (
          <button
            key={o.value}
            role="tab"
            aria-selected={on}
            onClick={() => onChange(o.value)}
            className={cn(
              "relative rounded-md px-4 py-2 text-sm font-medium transition-colors",
              on ? "text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {on &&
              (reduced ? (
                <span className="absolute inset-0 rounded-md bg-secondary" />
              ) : (
                <motion.span
                  layoutId={`segmented-${id}`}
                  className="absolute inset-0 rounded-md bg-secondary"
                  transition={{ type: "spring", stiffness: 400, damping: 34 }}
                />
              ))}
            <span className="relative">{o.label}</span>
          </button>
        );
      })}
    </div>
  );
}
