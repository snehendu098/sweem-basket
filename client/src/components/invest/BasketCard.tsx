"use client";

import Link from "next/link";
import { displayAsset, fmtPctOrDash } from "@/lib/api";
import { TokenIcon } from "@/components/TokenIcon";
import { ownership, type BasketSummary } from "@/lib/types";

export function BasketCard({
  basket,
  apy,
}: {
  basket: BasketSummary;
  apy: number | null;
}) {
  const assets = (basket.weights ?? []).map((w) => w.asset);
  const status = ownership(basket);
  return (
    <Link
      href={`/baskets/${basket.id}`}
      className="flex w-[270px] shrink-0 flex-col justify-between gap-6 rounded-xl border border-border bg-card p-5 transition-colors hover:border-input"
    >
      <div className="group/row flex items-start gap-3">
        {/* The stack collapses while the name is hovered: with four assets it
            takes the room the name needs. */}
        <span
          aria-hidden
          className="flex max-w-[120px] shrink-0 -space-x-2 transition-[max-width,opacity,transform] duration-300 ease-out group-has-[[data-name]:hover]/row:max-w-0 group-has-[[data-name]:hover]/row:-translate-x-3 group-has-[[data-name]:hover]/row:opacity-0"
        >
          {assets.length > 0 ? (
            assets.slice(0, 3).map((a) => (
              <TokenIcon
                key={a}
                symbol={a}
                size={36}
                className="rounded-full ring-2 ring-card transition-transform duration-200 ease-out hover:z-10 hover:-translate-y-1"
              />
            ))
          ) : (
            <span className="grid size-9 place-items-center rounded-full bg-secondary text-sm font-semibold">
              {basket.name.trim().charAt(0).toUpperCase() || "?"}
            </span>
          )}
        </span>
        <div data-name className="min-w-0 flex-1">
          <p className="truncate text-sm font-semibold">{basket.name}</p>
          <p className="truncate text-xs text-muted-foreground">
            {assets.length > 0 ? assets.map(displayAsset).join(" · ") : "no assets"}
          </p>
        </div>
        {status && status !== "not joined" && (
          <span className="ml-auto shrink-0 rounded-full bg-positive/15 px-2 py-0.5 text-[11px] whitespace-nowrap text-positive transition-[max-width,opacity,transform] duration-300 ease-out group-has-[[data-name]:hover]/row:max-w-0 group-has-[[data-name]:hover]/row:translate-x-3 group-has-[[data-name]:hover]/row:overflow-hidden group-has-[[data-name]:hover]/row:px-0 group-has-[[data-name]:hover]/row:opacity-0">
            {status}
          </span>
        )}
      </div>

      <div className="flex items-baseline gap-2">
        <span className="tnum text-4xl font-medium tracking-tight">
          {fmtPctOrDash(apy)}
        </span>
        <span className="text-sm text-muted-foreground">APY</span>
      </div>
    </Link>
  );
}
