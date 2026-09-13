"use client";

import { useRef } from "react";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { Skeleton } from "@/components/ui";
import { BasketCard } from "./BasketCard";
import type { BasketSummary } from "@/lib/types";

export function FeaturedCarousel({
  baskets,
  apys,
  loading,
}: {
  baskets: BasketSummary[];
  apys: Map<string, number | null>;
  loading?: boolean;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);

  const scroll = (dir: -1 | 1) =>
    scrollRef.current?.scrollBy({ left: dir * 290, behavior: "smooth" });

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="inline-flex items-center rounded-full border border-border bg-card px-5 py-2 text-sm font-medium">
          Featured baskets
        </span>
        <div className="flex items-center gap-2">
          <Arrow label="Scroll left" onClick={() => scroll(-1)}>
            <ArrowLeft className="size-4" />
          </Arrow>
          <Arrow label="Scroll right" onClick={() => scroll(1)}>
            <ArrowRight className="size-4" />
          </Arrow>
        </div>
      </div>

      <div ref={scrollRef} className="no-scrollbar flex gap-3 overflow-x-auto">
        {loading
          ? [0, 1, 2, 3].map((i) => (
              <div
                key={i}
                className="flex h-[164px] w-[270px] shrink-0 flex-col justify-between rounded-xl border border-border bg-card p-5"
              >
                <div className="flex items-start gap-3">
                  <Skeleton className="size-9 rounded-full" />
                  <div className="flex-1 space-y-2">
                    <Skeleton className="h-4 w-28" />
                    <Skeleton className="h-3 w-20" />
                  </div>
                </div>
                <Skeleton className="h-9 w-32" />
              </div>
            ))
          : baskets.map((b) => (
              <BasketCard key={b.id} basket={b} apy={apys.get(b.id) ?? null} />
            ))}
      </div>
    </div>
  );
}

function Arrow({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      onClick={onClick}
      className="flex size-10 cursor-pointer items-center justify-center rounded-full border border-border bg-card text-muted-foreground transition-colors hover:text-foreground"
    >
      {children}
    </button>
  );
}
