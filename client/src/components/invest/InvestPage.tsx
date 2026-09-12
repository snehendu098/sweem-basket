"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { blendApy, market, publicBaskets, sameChain } from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import { ErrorBox, Spinner } from "@/components/ui";
import { HeroSection } from "./HeroSection";
import { FeaturedCarousel } from "./FeaturedCarousel";
import { ALL_ASSETS, FilterBar } from "./FilterBar";
import { BasketTable } from "./BasketTable";
import type { Basket, BasketSummary } from "@/lib/types";

const FEATURED = 8;

export function InvestPage() {
  const { authenticated } = useSession();
  const chain = useChain();
  const [asset, setAsset] = useState(ALL_ASSETS);
  const [search, setSearch] = useState("");

  // Browsing needs no session. Once there is one we switch to the authed list,
  // which is the only one carrying `subscribed`.
  const anon = useAsync<BasketSummary[]>(`public-baskets:${authenticated}`, () =>
    authenticated ? Promise.resolve([]) : publicBaskets(),
  );
  const authed = useApi<Basket[] | null>(
    authenticated ? "/v1/baskets?scope=public" : null,
  );
  const summaries = useAsync(`assets:${chain.label}`, () => market.assets());

  // Both networks come back in one list, so the selected chain is what picks
  // the rows apart. `chain.label` is in the deps because switching networks has
  // to re-run this, not just re-render around it.
  const baskets: BasketSummary[] = useMemo(
    () =>
      (authenticated ? (authed.data ?? []) : (anon.data ?? [])).filter((b) =>
        sameChain(b.chain, chain.label),
      ),
    [authenticated, authed.data, anon.data, chain.label],
  );
  const loading = authenticated ? authed.loading : anon.loading;
  const error = authenticated ? authed.error : anon.error;

  const assetRows = useMemo(
    () => summaries.data?.assets ?? [],
    [summaries.data],
  );
  // One /assets call covers every basket: no /plan request per card.
  const apys = useMemo(
    () => new Map(baskets.map((b) => [b.id, blendApy(b.weights, assetRows)])),
    [baskets, assetRows],
  );

  const filterable = useMemo(
    () =>
      [...new Set(baskets.flatMap((b) => (b.weights ?? []).map((w) => w.asset)))].sort(),
    [baskets],
  );

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return baskets.filter((b) => {
      if (
        asset !== ALL_ASSETS &&
        !(b.weights ?? []).some((w) => w.asset === asset)
      ) {
        return false;
      }
      return q === "" || b.name.toLowerCase().includes(q);
    });
  }, [baskets, asset, search]);

  // Featured is the highest APY first; baskets with no computable APY sink.
  const featured = useMemo(
    () =>
      [...filtered]
        .sort((a, b) => (apys.get(b.id) ?? -1) - (apys.get(a.id) ?? -1))
        .slice(0, FEATURED),
    [filtered, apys],
  );

  const bestApy = assetRows.length
    ? Math.max(...assetRows.map((a) => a.best_apy))
    : null;

  return (
    <div className="w-full space-y-8 px-4 py-10">
      <HeroSection
        basketCount={baskets.length}
        venueCount={assetRows.reduce((s, a) => s + a.venues, 0)}
        bestApy={bestApy}
      />

      {error && <ErrorBox message={error} />}
      {loading && <Spinner label="Loading baskets…" />}

      {featured.length > 0 && (
        <FeaturedCarousel baskets={featured} apys={apys} />
      )}

      <FilterBar
        asset={asset}
        assets={filterable}
        search={search}
        onAssetChange={setAsset}
        onSearchChange={setSearch}
      />

      <BasketTable
        baskets={filtered}
        apys={apys}
        empty={
          baskets.length === 0 ? (
            // Not an empty table: an empty table on the wrong network looks
            // broken, and the answer is either to switch or to make one.
            <>
              No baskets on {chain.name} yet.{" "}
              <Link href="/" className="text-foreground underline underline-offset-4">
                Create one
              </Link>
              .
            </>
          ) : (
            "No basket matches these filters."
          )
        }
      />

      {summaries.error && (
        <p className="text-xs text-destructive">{summaries.error}</p>
      )}
    </div>
  );
}
