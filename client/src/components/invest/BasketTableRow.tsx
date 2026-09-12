"use client";

import { useRouter } from "next/navigation";
import { displayAsset, fmtBps, fmtPctOrDash } from "@/lib/api";
import { TokenIcon } from "@/components/TokenIcon";
import { ownership, type BasketSummary } from "@/lib/types";

export function BasketTableRow({
  rank,
  basket,
  apy,
}: {
  rank: number;
  basket: BasketSummary;
  apy: number | null;
}) {
  const router = useRouter();
  const weights = basket.weights ?? [];
  const status = ownership(basket);
  return (
    <tr
      className="cursor-pointer border-b border-border transition-colors hover:bg-accent/50"
      onClick={() => router.push(`/baskets/${basket.id}`)}
    >
      <td className="tnum px-4 py-4 text-sm text-muted-foreground">{rank}</td>
      <td className="px-4 py-4">
        <div className="flex items-center gap-3">
          <span aria-hidden className="flex shrink-0 -space-x-2">
            {weights.length > 0 ? (
              weights.slice(0, 3).map((w) => (
                <TokenIcon
                  key={w.asset}
                  symbol={w.asset}
                  size={32}
                  className="rounded-full ring-2 ring-background"
                />
              ))
            ) : (
              <span className="grid size-8 place-items-center rounded-full bg-secondary text-xs font-semibold">
                {basket.name.trim().charAt(0).toUpperCase() || "?"}
              </span>
            )}
          </span>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{basket.name}</p>
            <p className="truncate text-xs text-muted-foreground">
              {weights.map((w) => displayAsset(w.asset)).join(" · ") || "no assets"}
            </p>
          </div>
        </div>
      </td>
      <td className="px-4 py-4">
        <div className="flex flex-wrap gap-1.5">
          {weights.map((w) => (
            <span
              key={w.asset}
              className="tnum rounded-md bg-secondary px-2 py-0.5 text-xs text-muted-foreground"
            >
              {displayAsset(w.asset)} {fmtBps(w.weight_bps)}
            </span>
          ))}
        </div>
      </td>
      <td className="tnum px-4 py-4 text-sm font-medium text-positive">
        {fmtPctOrDash(apy)}
      </td>
      <td className="px-4 py-4 text-sm">
        {status === null ? null : status === "not joined" ? (
          <span className="text-xs text-muted-foreground">{status}</span>
        ) : (
          <span className="rounded-full bg-positive/15 px-2 py-0.5 text-xs whitespace-nowrap text-positive">
            {status}
          </span>
        )}
      </td>
    </tr>
  );
}
