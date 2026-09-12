"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import {
  QUOTE_ASSET,
  assetGroups,
  assetName,
  assetWarning,
  blendApy,
  displayAsset,
  displayProject,
  evenSplit,
  fmtPct,
  fmtUsd,
  groupOf,
  marketAssets,
  marketVenues,
  reachableAssets,
  reachableVenues,
  remapSelection,
  swapPaths,
  swappableAssets,
  venueWarning,
  venuesById,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import {
  Button,
  Divider,
  ErrorBox,
  Label,
  Panel,
  Spinner,
} from "@/components/ui";
import { TokenPicker, type PickOption } from "@/components/TokenPicker";
import { SettleDetail, toastError, useSettleToast } from "@/components/SettleToast";
import { TokenIcon } from "@/components/TokenIcon";
import { CountUp, Reveal } from "@/components/motion";
import {
  TOTAL_BPS,
  type Basket,
  type Plan,
  type Portfolio,
  type SettleResult,
} from "@/lib/types";

type Phase = "idle" | "creating" | "funding" | "funded";
type Mode = "simple" | "advanced";

type Choice = PickOption & { venueId?: string; family?: boolean };

export default function Create() {
  const { ready, authenticated, wallet, me, login, createWallet, delegate, api } =
    useSession();
  const chain = useChain();
  const router = useRouter();

  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [mode, setMode] = useState<Mode>("simple");
  const [dropped, setDropped] = useState<string[]>([]);
  const [amount, setAmount] = useState("1000");
  const [phase, setPhase] = useState<Phase>("idle");
  const [created, setCreated] = useState<Basket | null>(null);
  const [fundFailed, setFundFailed] = useState(false);
  const { notify, detail, clear } = useSettleToast();

  const assets = useAsync(`assets:${chain.label}`, () => marketAssets());
  const swaps = useAsync(`swap-paths:${chain.label}`, swapPaths);
  const swappable = swaps.data
    ? swappableAssets(swaps.data, chain.chainId)
    : null;
  // Advanced only: the summary now carries the split rate, so warnings need no join.
  const venues = useAsync(`venues:${chain.label}:${mode}`, () =>
    mode === "advanced" ? marketVenues() : Promise.resolve(null),
  );
  const byVenue = venues.data ? venuesById(venues.data.venues) : null;
  const list = reachableAssets(assets.data?.assets ?? [], swappable);
  const portfolio = useApi<Portfolio>("/v1/portfolio");
  const balance = (portfolio.data?.onchain?.balances ?? []).find(
    (b) => b.symbol === QUOTE_ASSET,
  );

  const parsed = Number(amount);
  const amountValid = Number.isFinite(parsed) && parsed > 0;
  const plan = useApi<Plan>(
    created && amountValid
      ? `/v1/baskets/${created.id}/plan?amount_usd=${parsed}`
      : null,
  );

  const groups = assetGroups(list, assets.data?.families);

  const simple: Choice[] = groups.map((g) => {
    const a = g.best;
    const sym = g.family ? g.id : displayAsset(a.asset);
    return {
      id: g.id,
      asset: a.asset,
      family: g.family,
      symbol: sym,
      title: assetName(sym),
      sub: g.family ? `${sym} · via ${displayAsset(a.asset)}` : displayAsset(a.asset),
      apy: a.best_apy,
      tvl: g.members.reduce((t, m) => t + m.total_tvl_usd, 0),
      disabled: false,
      reason: "",
      warning: assetWarning(a) ?? undefined,
    };
  });

  const taken = new Map<string, string>();
  if (mode === "advanced") {
    for (const id of picked) {
      const v = byVenue?.get(id);
      if (v && !taken.has(v.asset)) taken.set(v.asset, id);
    }
  }
  const advanced: Choice[] = reachableVenues(venues.data?.venues ?? [], swappable)
    .sort(
      (a, b) =>
        displayProject(a.project).localeCompare(displayProject(b.project)) ||
        b.apy - a.apy ||
        a.id.localeCompare(b.id),
    )
    .map((v) => {
      const clash = taken.get(v.asset);
      return {
        id: v.id,
        asset: v.asset,
        symbol: displayAsset(v.asset),
        title: assetName(v.asset),
        sub: `${displayProject(v.project)} · ${v.symbol}`,
        apy: v.apy,
        tvl: v.tvl_usd,
        group: displayProject(v.project),
        venueId: v.id,
        disabled: clash !== undefined && clash !== v.id,
        reason: `${displayAsset(v.asset)} is already held by another venue in this basket`,
        warning: venueWarning(v) ?? undefined,
      };
    });

  const options = mode === "simple" ? simple : advanced;
  const tiles = [...options]
    .filter((o) => !o.disabled)
    .sort((a, b) => b.tvl - a.tvl)
    .slice(0, 5);
  const labelOf = (id: string) => {
    const o = options.find((x) => x.id === id);
    if (o) return `${o.symbol} (${o.sub})`;
    const v = byVenue?.get(id);
    return v ? `${displayAsset(v.asset)} (${displayProject(v.project)})` : id;
  };

  const chosen = options.filter((o) => picked.includes(o.id) && !o.disabled);
  const settling = mode === "advanced" ? venues.loading : assets.loading;
  const orphans = settling
    ? []
    : picked.filter((id) => !options.some((o) => o.id === id));
  const split = evenSplit(chosen.map((o) => o.asset)).map((w, i) =>
    chosen[i].venueId ? { ...w, venue_id: chosen[i].venueId } : w,
  );
  const pct = evenSplit(chosen.map((o) => o.asset), 100);
  const apy = blendApy(
    split,
    chosen.map((o) => ({ asset: o.asset, best_apy: o.apy })),
  );

  // Venue rows may still be loading, so a mapped id is verified against the
  // rendered options below rather than here.
  function switchMode(next: Mode) {
    if (next === mode) return;
    const toId =
      next === "advanced"
        ? (id: string) => groups.find((x) => x.id === id)?.best.best_venue ?? null
        : (id: string) => groupOf(byVenue?.get(id)?.asset ?? "", groups)?.id ?? null;
    const res = remapSelection(picked, toId);
    setDropped(res.dropped.map(labelOf));
    setPicked(res.kept);
    setMode(next);
  }

  const busy = phase === "creating" || phase === "funding";
  const canSubmit = name.trim() !== "" && chosen.length > 0 && amountValid;
  const locked = created !== null;

  const step = !ready
    ? "loading"
    : !authenticated
      ? "connect"
      : !wallet
        ? "wallet"
        : !me
          ? "syncing"
          : !me.delegated
            ? "delegate"
            : "ready";

  const toggle = (id: string) => {
    setDropped([]);
    setPicked((p) => (p.includes(id) ? p.filter((a) => a !== id) : [...p, id]));
  };

  function reset() {
    setCreated(null);
    setPhase("idle");
    clear();
    setFundFailed(false);
  }

  async function fund(id: string) {
    setPhase("funding");
    clear();
    setFundFailed(false);
    try {
      const res = await api<SettleResult>(`/v1/baskets/${id}/deposit`, {
        method: "POST",
        body: JSON.stringify({ amount_usd: parsed }),
      });
      setPhase("funded");
      notify(res.status, res.data, { key: id, verb: "Deposited" });
      portfolio.reload();
      if (res.status !== 207) router.push(`/baskets/${id}`);
    } catch (e) {
      toastError(e);
      setFundFailed(true);
      setPhase("idle");
      portfolio.reload();
    }
  }

  async function submit() {
    if (step === "connect") return login();
    if (step === "wallet") return createWallet();
    if (step === "delegate") return delegate();
    if (phase === "funded" && created) {
      router.push(`/baskets/${created.id}`);
      return;
    }
    if (created) return fund(created.id);

    setPhase("creating");
    clear();
    setFundFailed(false);
    let basket: Basket;
    try {
      const res = await api<Basket>("/v1/baskets", {
        method: "POST",
        body: JSON.stringify({
          name: name.trim(),
          description: "",
          chain: chain.label,
          is_public: true,
          fee_bps: 0,
          weights: split,
        }),
      });
      basket = res.data;
    } catch (e) {
      toastError(e);
      setPhase("idle");
      return;
    }
    setCreated(basket);
    await fund(basket.id);
  }

  const legs = plan.data?.legs ?? null;

  return (
    <main className="w-full px-4 pb-24 pt-8">
      <Reveal className="mx-auto w-full max-w-lg space-y-6">
        <h1 className="text-lg font-medium">Create a basket</h1>

        <div className="flex items-stretch gap-6 px-1">
          <div className="flex-1">
            <div className="text-sm text-muted-foreground">Blended APY</div>
            <div className="tnum mt-1 text-3xl font-medium tracking-tight text-positive">
              {apy === null ? (
                <span className="text-muted-foreground">—</span>
              ) : (
                <CountUp value={apy} format={(n) => fmtPct(n)} />
              )}
            </div>
          </div>
          <div className="w-px self-stretch bg-border" />
          <div className="flex-1">
            <div className="text-sm text-muted-foreground">Rewards / year</div>
            <div className="tnum mt-1 text-3xl font-medium tracking-tight">
              {amountValid && apy !== null ? fmtUsd((parsed * apy) / 100) : "—"}
            </div>
          </div>
        </div>

        <Panel>
          <div className="p-5">
            <Label>Name</Label>
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Stable Core"
              aria-label="Basket name"
              disabled={locked}
              className="mt-3 w-full bg-transparent text-2xl font-medium tracking-tight outline-none placeholder:text-muted-foreground/40 disabled:text-muted-foreground"
            />
          </div>

          <Divider />

          <div className="p-5">
            <div className="flex items-center justify-between">
              <Label>Tokens</Label>
              <button
                type="button"
                role="switch"
                aria-checked={mode === "advanced"}
                disabled={locked}
                onClick={() => switchMode(mode === "simple" ? "advanced" : "simple")}
                className="text-xs text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
              >
                Advanced
                <span
                  className={`ml-2 inline-block h-1.5 w-1.5 rounded-full align-middle ${
                    mode === "advanced" ? "bg-foreground" : "bg-border"
                  }`}
                />
              </button>
            </div>
            {assets.loading ? (
              <div className="mt-4">
                <Spinner label="Loading tokens…" />
              </div>
            ) : assets.error ? (
              <div className="mt-4">
                <ErrorBox message={assets.error} />
              </div>
            ) : options.length === 0 ? (
              <p className="mt-4 text-sm text-muted-foreground">
                {mode === "advanced"
                  ? venues.error ?? `No venues indexed on ${chain.name} yet.`
                  : `No tokens indexed on ${chain.name} yet.`}
              </p>
            ) : (
              <Reveal key={mode} className="mt-4" y={4}>
                <TokenPicker
                  heading={mode === "simple" ? "Select tokens" : "Select venues"}
                  placeholder={mode === "simple" ? "Select tokens" : "Select venues"}
                  options={options}
                  tiles={tiles}
                  values={picked}
                  onToggle={toggle}
                  disabled={locked}
                />
              </Reveal>
            )}

            {chosen.some((o) => o.family) && (
              <p className="mt-3 text-xs text-muted-foreground">
                the instrument shown is fixed at deposit and not re-picked afterwards
              </p>
            )}

            {(dropped.length > 0 || orphans.length > 0) && (
              <p className="mt-3 text-xs text-warning">
                not carried over to {mode} mode:{" "}
                {[...dropped, ...orphans.map(labelOf)].join(", ")} — no reachable{" "}
                {mode === "simple" ? "family" : "venue"} matches, reselect if you want
                them
              </p>
            )}

            {swaps.error && (
              <p className="mt-3 text-xs text-warning">
                could not read the swap allowlist ({swaps.error}) — nothing is
                hidden, so a token with no route will surface at deposit instead
                of here
              </p>
            )}

            {chosen.length > 0 && (
              <div className="mt-5 space-y-1.5">
                {pct.map((w) => (
                  <div
                    key={w.asset}
                    className="flex items-center justify-between text-sm"
                  >
                    <span className="inline-flex items-center gap-2">
                      <TokenIcon symbol={w.asset} size={18} />
                      <span className="text-muted-foreground">
                        {displayAsset(w.asset)}
                      </span>
                    </span>
                    <span className="tnum">{w.weight_bps}%</span>
                  </div>
                ))}
              </div>
            )}
          </div>

          <Divider />

          <div className="p-5">
            <div className="flex items-center justify-between">
              <Label>Deposit</Label>
              {balance && (
                <button
                  type="button"
                  onClick={() => setAmount(String(balance.value))}
                  className="tnum text-sm text-muted-foreground transition-colors hover:text-foreground"
                >
                  {balance.value} {balance.symbol}
                </button>
              )}
            </div>
            <div className="mt-4 flex items-center gap-3">
              <input
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                inputMode="decimal"
                aria-label="Amount in USDC"
                placeholder="0.00"
                className="tnum w-full bg-transparent text-4xl font-medium tracking-tight outline-none placeholder:text-muted-foreground/40"
              />
              <span className="inline-flex shrink-0 items-center gap-2 rounded-full bg-secondary/70 py-2 pl-3 pr-4 text-sm font-medium">
                <TokenIcon symbol={QUOTE_ASSET} size={20} />
                {QUOTE_ASSET}
              </span>
            </div>
          </div>

          <Divider />

          <div className="p-5">
            <Label>Routing</Label>
            {chosen.length === 0 ? (
              <p className="mt-4 text-sm text-muted-foreground">
                Pick a token to see where your USDC would go.
              </p>
            ) : legs ? (
              <ul className="mt-4 space-y-3">
                {legs.map((l) => (
                  <li key={l.asset} className="text-sm">
                    <div className="flex items-center gap-3">
                      <span className="inline-flex items-center gap-2">
                        <TokenIcon symbol={l.asset} size={18} />
                        {displayAsset(l.asset)}
                      </span>
                      <span className="tnum ml-auto">{fmtUsd(l.amount_usd)}</span>
                      <span className="tnum w-24 truncate text-right text-xs text-muted-foreground">
                        {l.venue?.project ?? "—"}
                      </span>
                      <span className="tnum w-16 text-right text-xs text-positive">
                        {l.venue ? fmtPct(l.venue.apy) : "—"}
                      </span>
                    </div>
                    {l.price_usd === null && (
                      <div className="mt-1 text-xs text-warning">
                        value unknown — {l.reason || "no price feed"}
                      </div>
                    )}
                  </li>
                ))}
              </ul>
            ) : (
              <ul className="mt-4 space-y-3">
                {split.map((w) => {
                  const a = list.find((x) => x.asset === w.asset);
                  return (
                    <li key={w.asset} className="flex items-center gap-3 text-sm">
                      <span className="inline-flex items-center gap-2">
                        <TokenIcon symbol={w.asset} size={18} />
                        {displayAsset(w.asset)}
                      </span>
                      <span className="tnum ml-auto">
                        {amountValid
                          ? fmtUsd((parsed * w.weight_bps) / TOTAL_BPS)
                          : "—"}
                      </span>
                      <span className="tnum w-24 truncate text-right text-xs text-muted-foreground">
                        {a?.best_venue ?? "—"}
                      </span>
                      <span className="tnum w-16 text-right text-xs text-positive">
                        {a ? fmtPct(a.best_apy) : "—"}
                      </span>
                    </li>
                  );
                })}
              </ul>
            )}
            {plan.error && <p className="mt-3 text-xs text-warning">{plan.error}</p>}
          </div>

          <div className="p-5 pt-0">
            <Button
              className="w-full py-3"
              disabled={
                busy ||
                step === "loading" ||
                step === "syncing" ||
                (step === "ready" && !canSubmit)
              }
              onClick={() => void submit()}
            >
              {phase === "creating"
                ? "Creating basket…"
                : phase === "funding"
                  ? `Depositing ${amountValid ? fmtUsd(parsed) : ""}…`
                  : step === "connect"
                    ? "Connect"
                    : step === "wallet"
                      ? "Create wallet"
                      : step === "delegate"
                        ? "Enable delegation"
                        : step === "syncing" || step === "loading"
                          ? "…"
                          : phase === "funded"
                            ? "Open basket"
                            : created
                              ? `Retry deposit${amountValid ? ` ${fmtUsd(parsed)}` : ""}`
                              : `Create & deposit${amountValid ? ` ${fmtUsd(parsed)}` : ""}`}
            </Button>
          </div>
        </Panel>

        {created && fundFailed && (
          <div className="space-y-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-warning">
            <p>
              “{created.name}” was created and is empty — the deposit did not go
              through. Retry funds this basket; it will not create another.
            </p>
            <div className="flex gap-4">
              <Link href={`/baskets/${created.id}`} className="underline underline-offset-2">
                Open the basket
              </Link>
              <button type="button" onClick={reset} className="underline underline-offset-2">
                Start a different basket
              </button>
            </div>
          </div>
        )}

        {detail && <SettleDetail detail={detail} onClose={clear} />}
        {phase === "funded" && created && (
          <Link
            href={`/baskets/${created.id}`}
            className="inline-block text-sm text-muted-foreground underline underline-offset-2 hover:text-foreground"
          >
            Open “{created.name}”
          </Link>
        )}
      </Reveal>
    </main>
  );
}
