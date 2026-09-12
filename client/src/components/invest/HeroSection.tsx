"use client";

import { fmtPctOrDash } from "@/lib/api";
import { useChain } from "@/lib/chain";

export function HeroSection({
  basketCount,
  venueCount,
  bestApy,
}: {
  basketCount: number;
  venueCount: number;
  bestApy: number | null;
}) {
  const chain = useChain();
  return (
    <div className="w-full rounded-2xl border border-border bg-gradient-to-b from-card to-background p-10">
      <h1 className="text-4xl font-bold tracking-tight">
        Baskets other people built
      </h1>
      <p className="mt-4 max-w-lg text-base text-muted-foreground">
        Each basket is a set of stablecoin weights. Deposit into one and every
        slice is routed to the highest-APY venue indexed for its asset.
      </p>

      <div className="flex items-center gap-8 pt-8">
        <Stat value={String(basketCount)} label="Public baskets" />
        <div className="h-10 w-px bg-border" />
        <Stat value={String(venueCount)} label={`Venues indexed on ${chain.name}`} />
        <div className="h-10 w-px bg-border" />
        <Stat value={fmtPctOrDash(bestApy)} label="Best venue APY" />
      </div>
    </div>
  );
}

function Stat({ value, label }: { value: string; label: string }) {
  return (
    <div>
      <p className="tnum text-2xl font-bold">{value}</p>
      <p className="text-sm text-muted-foreground">{label}</p>
    </div>
  );
}
