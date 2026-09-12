"use client";

import { useRef } from "react";
import { ArrowLeft, ArrowRight } from "lucide-react";
import { BasketCard } from "./BasketCard";
import type { BasketSummary } from "@/lib/types";

export function FeaturedCarousel({
  baskets,
  apys,
}: {
  baskets: BasketSummary[];
  apys: Map<string, number | null>;
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
        {baskets.map((b) => (
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
