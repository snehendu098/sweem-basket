"use client";

import Link from "next/link";
import { useState } from "react";
import {
  displayAsset,
  displayProject,
  fmtPct,
  fmtTime,
  fmtUsd,
  sameChain,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useSession } from "@/lib/session";
import {
  Button,
  Divider,
  ErrorBox,
  Panel,
  Spinner,
} from "@/components/ui";
import { SettleDetail, toastError, useSettleToast } from "@/components/SettleToast";
import { TokenIcon } from "@/components/TokenIcon";
import { Reveal } from "@/components/motion";
import {
  IDLE_VENUE_ID,
  type Basket,
  type Holding,
  type Portfolio,
  type SettleResult,
} from "@/lib/types";

export default function PortfolioPage() {
  const { ready, authenticated, login, me, api } = useSession();
  const chain = useChain();
  const portfolio = useApi<Portfolio>("/v1/portfolio");
  const baskets = useApi<Basket[] | null>("/v1/baskets");

  const [busy, setBusy] = useState<string | null>(null);
  const { notify, detail, clear } = useSettleToast();

  const p = portfolio.data;
  // One account spans both Base networks. A Sepolia position added into a
  // mainnet total is a testnet dollar pretending to be a real one, so the
  // selected chain filters the rows — and then the totals are recomputed from
  // those rows rather than reusing the API's all-chain figures.
  const all = p?.positions ?? [];
  const positions = all.filter((h) => sameChain(h.chain, chain.label));
  const split = positions.length !== all.length;
  const totalUsd = split
    ? positions.reduce((s, h) => s + h.amount_usd, 0)
    : (p?.total_usd ?? 0);
  const blendedApy =
    split && totalUsd > 0
      ? positions.reduce((s, h) => s + (h.current_apy * h.amount_usd) / totalUsd, 0)
      : split
        ? 0
        : (p?.blended_apy ?? 0);
  const idleUsd = positions
    .filter((h) => h.venue_id === IDLE_VENUE_ID)
    .reduce((s, h) => s + h.amount_usd, 0);

  // One section per basket the user actually holds something in.
  const groups = new Map<string, Holding[]>();
  for (const h of positions) {
    const g = groups.get(h.basket_id);
    if (g) g.push(h);
    else groups.set(h.basket_id, [h]);
  }
  const nameOf = (id: string) =>
    (baskets.data ?? []).find((b) => b.id === id)?.name ?? "Basket";

  async function rebalance(basketId: string) {
    setBusy(basketId);
    clear();
    try {
      // Same route the keeper calls; with a user token it authenticates as the
      // user, and 207 is a normal partial outcome exactly like deposit.
      const res = await api<SettleResult>(
        `/v1/baskets/${basketId}/rebalance`,
        { method: "POST" },
      );
      notify(res.status, res.data, {
        key: basketId,
        verb: "Rebalanced",
        // A rebalance that moved nothing is a success, not an empty report.
        successTitle:
          res.data.moved_legs === 0
            ? `Nothing to move — no position cleared the drift threshold${
                res.data.threshold_apy !== undefined
                  ? ` of ${fmtPct(res.data.threshold_apy)}`
                  : ""
              }`
            : undefined,
      });
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(null);
      portfolio.reload();
    }
  }

  if (ready && !authenticated) {
    return (
      <main className="w-full px-4 pb-24 pt-8">
        <Panel className="mx-auto w-full max-w-2xl">
          <div className="flex items-center justify-between p-5">
            <span className="text-sm text-muted-foreground">
              Connect to see your positions.
            </span>
            <Button onClick={login}>Connect</Button>
          </div>
        </Panel>
      </main>
    );
  }

  return (
    <main className="w-full px-4 pb-24 pt-8">
      <Reveal className="mx-auto w-full max-w-2xl space-y-6">
        <h1 className="text-lg font-medium">Portfolio</h1>

        <div className="flex items-stretch gap-6 px-1">
          <div className="flex-1">
            <div className="text-sm text-muted-foreground">Total value</div>
            <div className="tnum mt-1 text-3xl font-medium tracking-tight">
              {p ? fmtUsd(totalUsd) : "—"}
            </div>
          </div>
          <div className="w-px self-stretch bg-border" />
          <div className="flex-1">
            <div className="text-sm text-muted-foreground">Blended APY</div>
            <div className="tnum mt-1 text-3xl font-medium tracking-tight text-positive">
              {p ? fmtPct(blendedApy) : "—"}
            </div>
          </div>
        </div>

        {idleUsd > 0 && (
          <div className="flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 p-4 text-sm text-warning">
            <span>{fmtUsd(idleUsd)} is idle in your wallet, in no venue.</span>
            <Link
              href="/invest"
              className="ml-auto underline underline-offset-4 hover:no-underline"
            >
              Browse baskets
            </Link>
          </div>
        )}

        {portfolio.loading && <Spinner label="Loading portfolio…" />}
        {portfolio.error && <ErrorBox message={portfolio.error} />}

        {p && positions.length === 0 && !portfolio.loading && (
          <Panel>
            <div className="flex items-center justify-between p-5 text-sm">
              {/* Says which network is empty: the other one may not be. */}
              <span className="text-muted-foreground">
                No positions on {chain.name} yet.
              </span>
              <Link
                href="/invest"
                className="text-foreground underline underline-offset-4 hover:no-underline"
              >
                Browse baskets
              </Link>
            </div>
          </Panel>
        )}

        {[...groups].map(([basketId, holdings]) => {
          // The best drift in the basket is what a rebalance would act on, so
          // it explains in advance whether the button will move anything.
          const bestDrift = holdings.reduce(
            (m, h) => (h.venue_id === IDLE_VENUE_ID ? m : Math.max(m, h.drift_apy)),
            0,
          );
          return (
            <section key={basketId} className="space-y-3">
              <div className="flex flex-wrap items-center gap-3 px-1">
                <h2 className="font-medium">{nameOf(basketId)}</h2>
                <span className="text-xs text-muted-foreground">
                  {bestDrift > 0
                    ? `best drift ${fmtPct(bestDrift)}`
                    : "no better venue than where you are"}
                </span>
                <Button
                  variant="ghost"
                  className="ml-auto"
                  disabled={busy !== null || !me?.delegated}
                  title={
                    me?.delegated
                      ? undefined
                      : "Enable delegation to let the executor move funds"
                  }
                  onClick={() => void rebalance(basketId)}
                >
                  {busy === basketId ? "Rebalancing…" : "Rebalance"}
                </Button>
              </div>

              <Panel>
                <ul className="divide-y divide-border">
                  {holdings.map((h) => (
                    <Row key={h.id} h={h} />
                  ))}
                </ul>
              </Panel>

              {detail?.key === basketId && (
                <SettleDetail detail={detail} onClose={clear} />
              )}
            </section>
          );
        })}

        {!me?.delegated && positions.length > 0 && (
          <p className="text-xs text-muted-foreground">
            Rebalancing needs delegation — grant it from the settings menu.
          </p>
        )}

        {p && !p.onchain_available && (
          <p className="text-xs text-muted-foreground">
            On-chain balances are unavailable right now, so amounts above come
            from our own records rather than a chain read.
          </p>
        )}
      </Reveal>
    </main>
  );
}

function Row({ h }: { h: Holding }) {
  const idle = h.venue_id === IDLE_VENUE_ID;
  // Drift is only meaningful against a placed position.
  const drift = !idle && h.drift_apy > 0;
  return (
    <li className="p-5">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="inline-flex items-center gap-2 font-medium">
          <TokenIcon symbol={h.asset} size={20} />
          {displayAsset(h.asset)}
        </span>
        <span className="text-xs text-muted-foreground">
          {idle ? "idle in wallet" : displayProject(h.project)}
        </span>
        <span className="tnum ml-auto">{fmtUsd(h.amount_usd)}</span>
        <span className="tnum w-16 text-right text-sm text-positive">
          {idle ? "—" : fmtPct(h.current_apy)}
        </span>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
        <span>entry {fmtPct(h.entry_apy)}</span>
        {/* A value the API could not produce is never turned into a number.
            The em dash stays rather than the cell disappearing: the amount on
            the line above is our own record, and a silently absent on-chain
            figure would read as one that agrees. The reason is still on the
            title, for anyone who wants it. */}
        <span
          className="tnum"
          title={
            h.onchain_usd === null
              ? h.value_reason || "no on-chain value for this holding"
              : undefined
          }
        >
          on-chain {h.onchain_usd === null ? "—" : fmtUsd(h.onchain_usd)}
        </span>
        <span className="ml-auto">{fmtTime(h.updated_at)}</span>
      </div>

      {drift && (
        <>
          <Divider />
          <div className="pt-2 text-xs text-warning">
            {fmtPct(h.drift_apy)} better available
            {h.best_venue ? ` at ${h.best_venue.project}` : ""} — rebalance to
            take it, or leave it to the keeper.
          </div>
        </>
      )}
    </li>
  );
}
