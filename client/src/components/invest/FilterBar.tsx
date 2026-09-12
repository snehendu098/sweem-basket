"use client";

import { Search } from "lucide-react";
import { displayAsset } from "@/lib/api";
import { Picker } from "@/components/ui";

export const ALL_ASSETS = "__all__";

/**
 * Two filters, both backed by data we have: the assets a basket weights, and
 * its name. Network and status dropdowns are omitted — one chain, and baskets
 * carry no status field, so they would be dead controls.
 */
export function FilterBar({
  asset,
  assets,
  search,
  onAssetChange,
  onSearchChange,
}: {
  asset: string;
  /** Asset tickers that appear in at least one listed basket. */
  assets: string[];
  search: string;
  onAssetChange: (v: string) => void;
  onSearchChange: (v: string) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Picker
        ariaLabel="Filter by asset"
        value={asset}
        onChange={onAssetChange}
        options={[
          { value: ALL_ASSETS, label: "Any asset" },
          ...assets.map((a) => ({ value: a, label: displayAsset(a) })),
        ]}
      />

      <label className="ml-auto flex items-center gap-2 rounded-full border border-border bg-card px-4 py-2">
        <Search className="size-4 shrink-0 text-muted-foreground" />
        <span className="sr-only">Search baskets by name</span>
        <input
          type="text"
          placeholder="Search by name"
          value={search}
          onChange={(e) => onSearchChange(e.target.value)}
          className="w-48 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        />
      </label>
    </div>
  );
}
