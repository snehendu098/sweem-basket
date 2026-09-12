"use client";

import { BasketTableRow } from "./BasketTableRow";
import type { BasketSummary } from "@/lib/types";

const HEADERS = ["#", "Basket", "Allocation", "APY", "Your status"];

export function BasketTable({
  baskets,
  apys,
  empty,
}: {
  baskets: BasketSummary[];
  apys: Map<string, number | null>;
  /** What to say when there is nothing to show. */
  empty: React.ReactNode;
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
          {baskets.length > 0 ? (
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
