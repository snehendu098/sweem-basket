"use client";

import { Skeleton } from "@/components/ui";
import { BasketTableRow } from "./BasketTableRow";
import type { BasketSummary } from "@/lib/types";

const HEADERS = ["#", "Basket", "Allocation", "APY", "Your status"];

export function BasketTable({
  baskets,
  apys,
  empty,
  loading,
}: {
  baskets: BasketSummary[];
  apys: Map<string, number | null>;
  empty: React.ReactNode;
  loading?: boolean;
}) {
  return (
    <div className="overflow-x-auto rounded-xl border border-border">
      <table className="w-full">
        <thead>
          <tr className="border-b border-border bg-accent/30">
            {HEADERS.map((h) => (
              <th
                key={h}
                className="px-4 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {loading ? (
            [0, 1, 2, 3, 4].map((i) => (
              <tr key={i} className="border-b border-border">
                <td className="px-4 py-4">
                  <Skeleton className="h-4 w-4" />
                </td>
                <td className="px-4 py-4">
                  <div className="flex items-center gap-3">
                    <Skeleton className="size-8 rounded-full" />
                    <Skeleton className="h-4 w-32" />
                  </div>
                </td>
                <td className="px-4 py-4">
                  <Skeleton className="h-4 w-28" />
                </td>
                <td className="px-4 py-4">
                  <Skeleton className="h-4 w-16" />
                </td>
                <td className="px-4 py-4">
                  <Skeleton className="h-4 w-20" />
                </td>
              </tr>
            ))
          ) : baskets.length > 0 ? (
            baskets.map((b, i) => (
              <BasketTableRow
                key={b.id}
                rank={i + 1}
                basket={b}
                apy={apys.get(b.id) ?? null}
              />
            ))
          ) : (
            <tr>
              <td
                colSpan={HEADERS.length}
                className="py-8 text-center text-sm text-muted-foreground"
              >
                {empty}
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
