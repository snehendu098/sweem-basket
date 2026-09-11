"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import {
  fmtBps,
  fmtCompactUsd,
  fmtPct,
  fmtUsd,
  fmtUsdOrUnknown,
} from "@/lib/api";
import { useApi, useSession } from "@/lib/session";
import {
  Button,
  Empty,
  ErrorBox,
  Panel,
  RequireAuth,
  SettleReport,
  Spinner,
  Stat,
} from "@/components/ui";
import type { Basket, Plan, PlanLeg, SettleResult } from "@/lib/types";

export default function BasketPage() {
  return (
    <RequireAuth>
      <Detail />
    </RequireAuth>
  );
}

function Detail() {
  const { id } = useParams<{ id: string }>();
  const { api, me } = useSession();
  const basket = useApi<Basket>(`/v1/baskets/${id}`);

  // Two values on purpose: what is typed, and what was actually planned.
  // The preview only changes when the user asks for it, so the routing shown
  // is always the routing that was computed for that exact amount.
  const [amount, setAmount] = useState("1000");
  const [previewed, setPreviewed] = useState(1000);
  const plan = useApi<Plan>(`/v1/baskets/${id}/plan?amount_usd=${previewed}`);

  const [action, setAction] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [result, setResult] = useState<{
    status: number;
    data: SettleResult;
  } | null>(null);
  const [subMsg, setSubMsg] = useState<string | null>(null);

  const parsed = Number(amount);
  const amountValid = Number.isFinite(parsed) && parsed > 0;

  async function run(
    path: string,
    method: "POST" | "DELETE",
    body?: unknown,
    label?: string,
  ) {
    setAction(label ?? path);
    setActionErr(null);
    setResult(null);
    setSubMsg(null);
    try {
      const res = await api<SettleResult>(`/v1/baskets/${id}${path}`, {
        method,
        ...(body ? { body: JSON.stringify(body) } : {}),
      });
      if (path === "/subscribe") {
        setSubMsg(method === "POST" ? "Subscribed." : "Exited.");
      } else {
        setResult({ status: res.status, data: res.data });
      }
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : String(e));
    } finally {
      setAction(null);
    }
  }

  if (basket.loading) return <Spinner label="Loading basket…" />;
  if (basket.error)
    return <ErrorBox message={basket.error} onRetry={basket.reload} />;
  if (!basket.data) return <Empty>Basket not found.</Empty>;

  const b = basket.data;
  const weights = b.weights ?? [];
  const legs = plan.data?.legs ?? [];
  const unpriced = legs.filter((l) => l.price_usd === null).length;
  const routable = legs.filter((l) => l.venue).length;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <Link href="/explore" className="text-xs text-zinc-500 hover:text-zinc-300">
            ← Baskets
          </Link>
          <h1 className="mt-1 text-2xl font-semibold tracking-tight">{b.name}</h1>
          <p className="mt-1 max-w-2xl text-sm text-zinc-500">
            {b.description || "No description."}
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="ghost"
            disabled={action !== null || !me?.delegated}
            title={
              me?.delegated ? undefined : "Delegation is required to subscribe"
            }
            onClick={() => void run("/subscribe", "POST", undefined, "subscribe")}
          >
            {action === "subscribe" ? "Subscribing…" : "Subscribe"}
          </Button>
          <Button
            variant="ghost"
            disabled={action !== null}
            onClick={() =>
              void run("/subscribe", "DELETE", undefined, "unsubscribe")
            }
          >
            Exit
          </Button>
          <Button
            variant="ghost"
            disabled={action !== null}
            onClick={() => void run("/rebalance", "POST", undefined, "rebalance")}
          >
            {action === "rebalance" ? "Rebalancing…" : "Rebalance"}
          </Button>
        </div>
      </div>

      {!me?.delegated && (
        <div className="rounded border border-amber-900 bg-amber-950/30 p-3 text-sm text-amber-300">
          You have not delegated yet, so nothing can execute from your wallet.{" "}
          <Link href="/onboarding" className="underline">
            Grant delegation
          </Link>
          . The preview below still works — it moves no money.
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="space-y-6">
          <Panel
            title="Routing preview"
            right={
              <span className="text-[11px] text-zinc-500">
                read-only · nothing is signed
              </span>
            }
          >
            <div className="flex flex-wrap items-end gap-3">
              <label className="block">
                <span className="text-[11px] uppercase tracking-wider text-zinc-500">
                  Amount (USD)
                </span>
                <input
                  value={amount}
                  onChange={(e) => setAmount(e.target.value)}
                  inputMode="decimal"
                  className="mt-1 w-40 rounded border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm outline-none focus:border-emerald-600"
                />
              </label>
              <Button
                variant="ghost"
                disabled={!amountValid || plan.loading}
                onClick={() => setPreviewed(parsed)}
              >
                {plan.loading ? "Planning…" : "Preview routing"}
              </Button>
              {plan.data && (
                <div className="ml-auto flex gap-8">
                  <Stat
                    label="Blended APY"
                    value={fmtPct(plan.data.blended_apy)}
                    tone="good"
                  />
                  <Stat
                    label="Routable legs"
                    value={`${routable}/${legs.length}`}
                    tone={routable === legs.length ? "default" : "warn"}
                  />
                </div>
              )}
            </div>

            <div className="mt-4">
              {plan.loading ? (
                <Spinner label="Resolving venues…" />
              ) : plan.error ? (
                <ErrorBox message={plan.error} onRetry={plan.reload} />
              ) : legs.length === 0 ? (
                <Empty>The plan returned no legs.</Empty>
              ) : (
                <ul className="divide-y divide-zinc-800 rounded border border-zinc-800">
                  {legs.map((l) => (
                    <LegPreview key={l.asset} leg={l} />
                  ))}
                </ul>
              )}
            </div>

            {unpriced > 0 && (
              <p className="mt-3 text-xs text-amber-400">
                {unpriced} leg{unpriced === 1 ? "" : "s"} could not be priced.
                Unpriceable assets are never routed and will be skipped on
                deposit — the protocol will not move money on a number it
                invented.
              </p>
            )}

            <div className="mt-4 flex items-center gap-3 border-t border-zinc-800 pt-4">
              <Button
                disabled={!amountValid || action !== null || !me?.delegated}
                onClick={() =>
                  void run(
                    "/deposit",
                    "POST",
                    { amount_usd: parsed },
                    "deposit",
                  )
                }
              >
                {action === "deposit"
                  ? "Executing…"
                  : `Deposit ${amountValid ? fmtUsd(parsed) : ""}`}
              </Button>
              <span className="text-xs text-zinc-500">
                Executes exactly the plan above, leg by leg.
              </span>
            </div>
          </Panel>

          {actionErr && <ErrorBox message={actionErr} />}
          {subMsg && (
            <div className="rounded border border-emerald-800 bg-emerald-950/30 p-3 text-sm text-emerald-300">
              {subMsg}
            </div>
          )}
          {result && (
            <Panel title="Execution result">
              <SettleReport result={result.data} status={result.status} />
            </Panel>
          )}
        </div>

        <aside className="space-y-4">
          <Panel title="Weights">
            {weights.length === 0 ? (
              <Empty>This basket has no weights.</Empty>
            ) : (
              <ul className="space-y-2">
                {weights.map((w) => (
                  <li key={w.asset}>
                    <div className="flex justify-between text-sm">
                      <span className="font-mono">{w.asset}</span>
                      <span className="font-mono text-zinc-400">
                        {fmtBps(w.weight_bps)}
                      </span>
                    </div>
                    <div className="mt-1 h-1.5 w-full overflow-hidden rounded bg-zinc-800">
                      <div
                        className="h-full bg-emerald-600"
                        style={{ width: `${w.weight_bps / 100}%` }}
                      />
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Panel>
          <Panel title="Basket">
            <dl className="space-y-2 text-xs">
              <div>
                <dt className="text-zinc-500">Chain</dt>
                <dd className="font-mono">{b.chain}</dd>
              </div>
              <div>
                <dt className="text-zinc-500">Fee</dt>
                <dd className="font-mono">{fmtBps(b.fee_bps)}</dd>
              </div>
              <div>
                <dt className="text-zinc-500">Visibility</dt>
                <dd className="font-mono">
                  {b.is_public ? "public" : "private"}
                </dd>
              </div>
              <div>
                <dt className="text-zinc-500">ID</dt>
                <dd className="break-all font-mono text-zinc-400">{b.id}</dd>
              </div>
            </dl>
          </Panel>
        </aside>
      </div>
    </div>
  );
}

/** One slice of the plan: where it routes, and why — or why it cannot. */
function LegPreview({ leg }: { leg: PlanLeg }) {
  const priced = leg.price_usd !== null;
  return (
    <li className="grid gap-2 p-3 sm:grid-cols-[8rem_1fr]">
      <div>
        <div className="font-mono text-sm text-zinc-100">{leg.asset}</div>
        <div className="font-mono text-xs text-zinc-500">
          {fmtBps(leg.weight_bps)} · {fmtUsd(leg.amount_usd)}
        </div>
      </div>
      <div className="min-w-0">
        {leg.venue ? (
          <>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span className="rounded border border-emerald-900 bg-emerald-950/40 px-1.5 py-0.5 text-xs text-emerald-300">
                {leg.venue.project}
              </span>
              <span className="font-mono text-sm text-emerald-400">
                {fmtPct(leg.venue.apy)} APY
              </span>
              <span className="text-xs text-zinc-500">
                base {fmtPct(leg.venue.apy_base)} · rewards{" "}
                {fmtPct(leg.venue.apy_reward)}
              </span>
              <span className="text-xs text-zinc-500">
                TVL {fmtCompactUsd(leg.venue.tvl_usd)}
              </span>
            </div>
            <div className="mt-1 break-all font-mono text-[11px] text-zinc-600">
              {leg.venue.id}
            </div>
          </>
        ) : (
          <div className="text-sm text-amber-400">not routed</div>
        )}
        <div className="mt-1 text-xs text-zinc-400">{leg.reason}</div>
        <div className="mt-1 font-mono text-[11px] text-zinc-500">
          {priced ? (
            <>
              price {fmtUsdOrUnknown(leg.price_usd)}
              {leg.amount_token !== null &&
                ` · ${leg.amount_token.toLocaleString("en-US", {
                  maximumFractionDigits: 6,
                })} ${leg.asset}`}
            </>
          ) : (
            <span className="text-amber-400">
              price unavailable · amount in tokens unknown
            </span>
          )}
        </div>
      </div>
    </li>
  );
}
