"use client";

import { animate, motion, useReducedMotion } from "motion/react";
import { useEffect, useState } from "react";
import { cn } from "@/lib/utils";

const EASE = [0.16, 1, 0.3, 1] as const;

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
