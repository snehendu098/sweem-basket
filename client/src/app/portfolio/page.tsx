"use client";

import Link from "next/link";
import { basescanAddress, fmtPct, fmtUsd, fmtUsdOrUnknown } from "@/lib/api";
import { useApi } from "@/lib/session";
import {
  Empty,
  ErrorBox,
  Panel,
  RequireAuth,
  Spinner,
  Stat,
} from "@/components/ui";
import { IDLE_VENUE_ID, type Holding, type Portfolio } from "@/lib/types";

export default function PortfolioPage() {
  return (
    <RequireAuth>
      <View />
    </RequireAuth>
  );
}

function View() {
  const { data, error, loading, reload } = useApi<Portfolio>("/v1/portfolio");

  if (loading) return <Spinner label="Loading portfolio…" />;
  if (error) return <ErrorBox message={error} onRetry={reload} />;
  if (!data) return <Empty>No portfolio data.</Empty>;

  const positions = data.positions ?? [];
  const idle = positions.filter((p) => p.venue_id === IDLE_VENUE_ID);
  const drifting = positions.filter((p) => p.drift_apy > 0);

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Portfolio</h1>
        <a
          href={basescanAddress(data.wallet_address)}
          target="_blank"
          rel="noreferrer"
          className="mt-1 inline-block break-all font-mono text-xs text-sky-400 hover:underline"
        >
          {data.wallet_address}
        </a>
      </div>

      <Panel>
        <div className="grid gap-6 sm:grid-cols-4">
          <Stat label="Total (our books)" value={fmtUsd(data.total_usd)} />
          <Stat
            label="Blended APY"
            value={fmtPct(data.blended_apy)}
            tone="good"
          />
          <Stat label="Positions" value={String(positions.length)} />
          <Stat
            label="Drifting"
            value={String(drifting.length)}
            tone={drifting.length > 0 ? "warn" : "default"}
            sub={drifting.length > 0 ? "a rebalance would earn more" : undefined}
          />
        </div>
        {!data.onchain_available && (
          <p className="mt-4 border-t border-zinc-800 pt-3 text-xs text-zinc-500">
            Onchain reconciliation is unavailable (the Token API is not
            configured on the server). The figures above are the protocol&apos;s
            own bookkeeping, not verified balances.
          </p>
        )}
      </Panel>

      {idle.length > 0 && (
        <div className="rounded border border-amber-800 bg-amber-950/30 p-4 text-sm text-amber-200">
          <div className="font-medium">
            {idle.length} position{idle.length === 1 ? "" : "s"} idle in your
            wallet
          </div>
          <p className="mt-1 text-amber-300/80">
            A rebalance withdrew these funds but the redeposit did not land. The
            money is safe in your own wallet and is earning nothing. Running a
            rebalance on the basket will place it again.
          </p>
          <ul className="mt-2 font-mono text-xs">
            {idle.map((p) => (
              <li key={p.id}>
                {p.asset} · {fmtUsd(p.amount_usd)} ·{" "}
                <Link
                  href={`/baskets/${p.basket_id}`}
                  className="text-amber-100 underline"
                >
                  rebalance
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}

      <Panel title="Positions">
        {positions.length === 0 ? (
          <Empty>
            No positions yet. Deposit into a basket to open one.
          </Empty>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-[11px] uppercase tracking-wider text-zinc-500">
                <tr>
                  <th className="pb-2 font-medium">Asset</th>
                  <th className="pb-2 font-medium">Venue</th>
                  <th className="pb-2 font-medium">Amount</th>
                  <th className="pb-2 font-medium">Entry</th>
                  <th className="pb-2 font-medium">Current</th>
                  <th className="pb-2 font-medium">Drift</th>
                  <th className="pb-2 font-medium">Onchain</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-zinc-800">
                {positions.map((p) => (
                  <Row key={p.id} p={p} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {data.onchain_available && data.onchain && (
        <Panel
          title={`Onchain balances (${data.onchain.chain})`}
          right={
            <span className="text-[11px] text-zinc-500">
              {data.onchain.token_count} tokens · via The Graph Token API
            </span>
          }
        >
          {(data.onchain.balances ?? []).length === 0 ? (
            <Empty>The wallet holds no indexed tokens.</Empty>
          ) : (
            <ul className="divide-y divide-zinc-800 text-sm">
              {(data.onchain.balances ?? []).map((b) => (
                <li
                  key={b.contract}
                  className="flex items-center gap-4 py-2 font-mono"
                >
                  <span className="w-24">{b.symbol}</span>
                  <span className="w-32 text-zinc-400">
                    {b.value.toLocaleString("en-US", {
                      maximumFractionDigits: 6,
                    })}
                  </span>
                  <span className="truncate text-[11px] text-zinc-600">
                    {b.contract}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Panel>
      )}
    </div>
  );
}

function Row({ p }: { p: Holding }) {
  const idle = p.venue_id === IDLE_VENUE_ID;
  return (
    <tr className={idle ? "bg-amber-950/20" : undefined}>
      <td className="py-2 font-mono">{p.asset}</td>
      <td className="py-2">
        {idle ? (
          <span className="rounded border border-amber-800 px-1.5 py-0.5 text-xs text-amber-300">
            idle in wallet
          </span>
        ) : (
          <>
            <div className="text-xs text-zinc-300">{p.project}</div>
            <div className="break-all font-mono text-[11px] text-zinc-600">
              {p.venue_id}
            </div>
          </>
        )}
      </td>
      <td className="py-2 font-mono">{fmtUsd(p.amount_usd)}</td>
      <td className="py-2 font-mono text-zinc-400">{fmtPct(p.entry_apy)}</td>
      <td className="py-2 font-mono text-emerald-400">
        {fmtPct(p.current_apy)}
      </td>
      <td
        className={`py-2 font-mono ${
          p.drift_apy > 0 ? "text-amber-400" : "text-zinc-500"
        }`}
      >
        {p.drift_apy > 0 ? `+${fmtPct(p.drift_apy)}` : fmtPct(p.drift_apy)}
        {p.best_venue && p.drift_apy > 0 && (
          <div className="text-[11px] text-zinc-500">→ {p.best_venue.project}</div>
        )}
      </td>
      <td className="py-2">
        <div
          className={`font-mono ${
            p.onchain_usd === null ? "text-zinc-500" : "text-zinc-200"
          }`}
        >
          {fmtUsdOrUnknown(p.onchain_usd)}
        </div>
        {p.value_reason && (
          <div className="max-w-56 text-[11px] leading-tight text-zinc-500">
            {p.value_reason}
          </div>
        )}
        {p.onchain_usd !== null && (
          <div
            className={`text-[11px] ${
              p.reconciled ? "text-emerald-500" : "text-amber-500"
            }`}
          >
            {p.reconciled ? "reconciled" : "does not match our books"}
          </div>
        )}
      </td>
    </tr>
  );
}
