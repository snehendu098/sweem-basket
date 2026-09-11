"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { CHAIN, fmtCompactUsd, fmtPct, market } from "@/lib/api";
import { useAsync, useSession } from "@/lib/session";
import {
  Button,
  Empty,
  ErrorBox,
  Panel,
  RequireAuth,
  Spinner,
} from "@/components/ui";
import { TOTAL_BPS, type Basket } from "@/lib/types";

export default function CreatePage() {
  return (
    <RequireAuth>
      <Create />
    </RequireAuth>
  );
}

function Create() {
  const { api } = useSession();
  const router = useRouter();
  const assets = useAsync("assets", () => market.assets());

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [isPublic, setIsPublic] = useState(true);
  const [feeBps, setFeeBps] = useState(0);
  const [weights, setWeights] = useState<Record<string, number>>({});
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const picked = Object.entries(weights);
  const total = picked.reduce((s, [, bps]) => s + bps, 0);
  const remainder = TOTAL_BPS - total;
  const hasZero = picked.some(([, bps]) => bps <= 0);
  const valid =
    name.trim() !== "" &&
    picked.length > 0 &&
    remainder === 0 &&
    !hasZero &&
    feeBps >= 0 &&
    feeBps <= TOTAL_BPS;

  function toggle(asset: string) {
    setWeights((w) => {
      const next = { ...w };
      if (asset in next) delete next[asset];
      else next[asset] = 0;
      return next;
    });
  }

  function setBps(asset: string, bps: number) {
    setWeights((w) => ({
      ...w,
      [asset]: Math.max(0, Math.min(TOTAL_BPS, Math.round(bps))),
    }));
  }

  function splitEvenly() {
    const keys = Object.keys(weights);
    if (keys.length === 0) return;
    const base = Math.floor(TOTAL_BPS / keys.length);
    const next: Record<string, number> = {};
    keys.forEach((k, i) => {
      next[k] = base + (i < TOTAL_BPS - base * keys.length ? 1 : 0);
    });
    setWeights(next);
  }

  async function submit() {
    setSubmitting(true);
    setError(null);
    try {
      const { data } = await api<Basket>("/v1/baskets", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          description: description.trim(),
          chain: CHAIN,
          is_public: isPublic,
          fee_bps: feeBps,
          weights: picked.map(([asset, weight_bps]) => ({
            asset,
            weight_bps,
          })),
        }),
      });
      router.push(`/baskets/${data.id}`);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setSubmitting(false);
    }
  }

  const list = assets.data?.assets ?? [];

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <div className="space-y-5">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">
            Create a basket
          </h1>
          <p className="mt-1 text-sm text-zinc-500">
            Weights are stored in basis points and must sum to exactly 10000.
          </p>
        </div>

        <Panel title="Details">
          <div className="space-y-3">
            <Field label="Name">
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Stable Core"
                className="w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-600"
              />
            </Field>
            <Field label="Description">
              <textarea
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={2}
                placeholder="What this basket is for."
                className="w-full rounded border border-zinc-700 bg-zinc-950 px-3 py-2 text-sm outline-none focus:border-emerald-600"
              />
            </Field>
            <div className="flex items-center gap-6">
              <Field label="Fee (bps)">
                <input
                  type="number"
                  min={0}
                  max={TOTAL_BPS}
                  value={feeBps}
                  onChange={(e) => setFeeBps(Number(e.target.value) || 0)}
                  className="w-28 rounded border border-zinc-700 bg-zinc-950 px-3 py-2 font-mono text-sm outline-none focus:border-emerald-600"
                />
              </Field>
              <label className="mt-5 flex items-center gap-2 text-sm text-zinc-300">
                <input
                  type="checkbox"
                  checked={isPublic}
                  onChange={(e) => setIsPublic(e.target.checked)}
                  className="accent-emerald-500"
                />
                Publish publicly
              </label>
            </div>
          </div>
        </Panel>

        <Panel
          title={`Assets on ${CHAIN}`}
          right={
            <button
              onClick={splitEvenly}
              disabled={picked.length === 0}
              className="text-xs text-zinc-400 hover:text-zinc-200 disabled:text-zinc-700"
            >
              Split evenly
            </button>
          }
        >
          {assets.loading ? (
            <Spinner label="Reading assets from market-data…" />
          ) : assets.error ? (
            <ErrorBox message={assets.error} />
          ) : list.length === 0 ? (
            <Empty>
              market-data reports no assets with live venues, so there is
              nothing to allocate.
            </Empty>
          ) : (
            <ul className="divide-y divide-zinc-800">
              {list.map((a) => {
                const on = a.asset in weights;
                return (
                  <li
                    key={a.asset}
                    className="flex items-center gap-3 py-2 text-sm"
                  >
                    <input
                      type="checkbox"
                      checked={on}
                      onChange={() => toggle(a.asset)}
                      className="accent-emerald-500"
                    />
                    <span className="w-20 font-mono">{a.asset}</span>
                    <span className="w-20 font-mono text-emerald-400">
                      {fmtPct(a.best_apy)}
                    </span>
                    <span className="w-24 font-mono text-xs text-zinc-500">
                      {fmtCompactUsd(a.total_tvl_usd)}
                    </span>
                    <span className="text-xs text-zinc-600">
                      {a.venues} venue{a.venues === 1 ? "" : "s"}
                    </span>
                    {on && (
                      <span className="ml-auto flex items-center gap-2">
                        <input
                          type="number"
                          min={0}
                          max={TOTAL_BPS}
                          step={100}
                          value={weights[a.asset]}
                          onChange={(e) =>
                            setBps(a.asset, Number(e.target.value) || 0)
                          }
                          className="w-24 rounded border border-zinc-700 bg-zinc-950 px-2 py-1 text-right font-mono text-sm outline-none focus:border-emerald-600"
                        />
                        <span className="w-14 text-right font-mono text-xs text-zinc-500">
                          {(weights[a.asset] / 100).toFixed(2)}%
                        </span>
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </Panel>
      </div>

      <aside className="space-y-4 lg:sticky lg:top-20 lg:self-start">
        <Panel title="Allocation">
          <div className="flex items-baseline justify-between">
            <span className="text-sm text-zinc-400">Allocated</span>
            <span className="font-mono text-lg">
              {total} / {TOTAL_BPS} bps
            </span>
          </div>
          <div className="mt-2 h-2 w-full overflow-hidden rounded bg-zinc-800">
            <div
              className={`h-full ${
                remainder === 0 ? "bg-emerald-500" : "bg-amber-500"
              }`}
              style={{ width: `${Math.min(100, (total / TOTAL_BPS) * 100)}%` }}
            />
          </div>
          <div
            className={`mt-2 font-mono text-sm ${
              remainder === 0
                ? "text-emerald-400"
                : remainder > 0
                  ? "text-amber-400"
                  : "text-red-400"
            }`}
          >
            {remainder === 0
              ? "balanced"
              : remainder > 0
                ? `${remainder} bps unallocated`
                : `${-remainder} bps over`}
          </div>

          {picked.length === 0 && (
            <p className="mt-3 text-xs text-zinc-500">
              Select at least one asset.
            </p>
          )}
          {hasZero && (
            <p className="mt-3 text-xs text-amber-400">
              Every selected asset needs a weight above zero.
            </p>
          )}

          {error && (
            <div className="mt-3">
              <ErrorBox message={error} />
            </div>
          )}

          <Button
            className="mt-4 w-full"
            disabled={!valid || submitting}
            onClick={() => void submit()}
          >
            {submitting ? "Creating…" : "Create basket"}
          </Button>
          <p className="mt-2 text-center text-[11px] text-zinc-600">
            Creating a basket moves no money.
          </p>
        </Panel>
      </aside>
    </div>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <label className="block">
      <span className="text-[11px] uppercase tracking-wider text-zinc-500">
        {label}
      </span>
      <div className="mt-1">{children}</div>
    </label>
  );
}
