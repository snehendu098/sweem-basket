"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { fmtBps, fmtPct } from "@/lib/api";
import { useApi, useSession } from "@/lib/session";
import { Empty, ErrorBox, Panel, RequireAuth, Spinner } from "@/components/ui";
import type { Basket, Plan } from "@/lib/types";

export default function ExplorePage() {
  return (
    <RequireAuth>
      <Explore />
    </RequireAuth>
  );
}

function Explore() {
  const [scope, setScope] = useState<"public" | "mine">("public");
  const { data, error, loading, reload } = useApi<Basket[] | null>(
    `/v1/baskets?scope=${scope}`,
  );
  const baskets = data ?? [];

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Baskets</h1>
          <p className="mt-1 text-sm text-zinc-500">
            A basket is a weight config. Subscribing points your own wallet at
            those weights.
          </p>
        </div>
        <Link
          href="/create"
          className="rounded bg-emerald-500 px-3 py-1.5 text-sm font-medium text-zinc-950 hover:bg-emerald-400"
        >
          Create basket
        </Link>
      </div>

      <div className="flex gap-1 text-sm">
        {(["public", "mine"] as const).map((s) => (
          <button
            key={s}
            onClick={() => setScope(s)}
            className={`rounded px-3 py-1 ${
              scope === s
                ? "bg-zinc-800 text-zinc-100"
                : "text-zinc-500 hover:text-zinc-300"
            }`}
          >
            {s === "public" ? "Public" : "Mine"}
          </button>
        ))}
      </div>

      {loading ? (
        <Spinner label="Loading baskets…" />
      ) : error ? (
        <ErrorBox message={error} onRetry={reload} />
      ) : baskets.length === 0 ? (
        <Empty>
          {scope === "public"
            ? "No public baskets have been published yet."
            : "You have not created a basket yet."}
        </Empty>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {baskets.map((b) => (
            <BasketCard key={b.id} basket={b} />
          ))}
        </div>
      )}
    </div>
  );
}

/**
 * The list endpoint does not carry weights, and blended APY is a live routing
 * result — so each card asks /plan for the real answer rather than estimating.
 */
function BasketCard({ basket }: { basket: Basket }) {
  const { api } = useSession();
  const [plan, setPlan] = useState<Plan | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    api<Plan>(`/v1/baskets/${basket.id}/plan?amount_usd=0`)
      .then(({ data }) => live && setPlan(data))
      .catch((e) => live && setErr(e instanceof Error ? e.message : String(e)));
    return () => {
      live = false;
    };
  }, [api, basket.id]);

  const legs = plan?.legs ?? [];

  return (
    <Link href={`/baskets/${basket.id}`} className="block">
      <Panel className="h-full transition hover:border-zinc-700">
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0">
            <h3 className="truncate font-medium text-zinc-100">{basket.name}</h3>
            <p className="mt-1 line-clamp-2 text-sm text-zinc-500">
              {basket.description || "No description."}
            </p>
          </div>
          <div className="shrink-0 text-right">
            <div className="text-[11px] uppercase tracking-wider text-zinc-500">
              Blended APY
            </div>
            <div className="font-mono text-xl text-emerald-400">
              {plan ? fmtPct(plan.blended_apy) : err ? "—" : "…"}
            </div>
          </div>
        </div>

        <div className="mt-4 flex flex-wrap gap-1.5">
          {legs.length > 0 ? (
            legs.map((l) => (
              <span
                key={l.asset}
                className={`rounded border px-1.5 py-0.5 font-mono text-[11px] ${
                  l.venue
                    ? "border-zinc-700 text-zinc-300"
                    : "border-amber-900 text-amber-400"
                }`}
                title={l.reason}
              >
                {l.asset} {fmtBps(l.weight_bps)}
              </span>
            ))
          ) : err ? (
            <span className="text-xs text-red-400">
              could not load weights: {err}
            </span>
          ) : (
            <span className="text-xs text-zinc-600">loading weights…</span>
          )}
        </div>

        <div className="mt-3 flex gap-3 text-[11px] text-zinc-600">
          <span>{basket.chain}</span>
          <span>fee {fmtBps(basket.fee_bps)}</span>
          <span>{basket.is_public ? "public" : "private"}</span>
        </div>
      </Panel>
    </Link>
  );
}
