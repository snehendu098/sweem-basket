"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import {
  QUOTE_ASSET,
  displayAsset,
  evenSplit,
  fmtPct,
  fmtUsd,
  isSwappable,
  marketAssets,
  swapPaths,
  swappableAssets,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import {
  Button,
  Divider,
  ErrorBox,
  Label,
  MultiPicker,
  Panel,
  Spinner,
  Tooltip,
} from "@/components/ui";
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

export default function Create() {
  const { ready, authenticated, wallet, me, login, createWallet, delegate, api } =
    useSession();
  const chain = useChain();
  const router = useRouter();

  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [amount, setAmount] = useState("1000");
  const [phase, setPhase] = useState<Phase>("idle");
  const [created, setCreated] = useState<Basket | null>(null);
  const [fundFailed, setFundFailed] = useState(false);
  const { notify, detail, clear } = useSettleToast();

  const assets = useAsync(`assets:${chain.label}`, () => marketAssets());
  const list = assets.data?.assets ?? [];
  const swaps = useAsync(`swap-paths:${chain.label}`, swapPaths);
  const swappable = swaps.data
    ? swappableAssets(swaps.data, chain.chainId)
    : null;
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

  const blocked = (asset: string) => !isSwappable(asset, swappable);
  const noRoute = `no USDC swap route on ${chain.name} — this token cannot be bought with your deposit here`;
  const many = list.length > 5;

  const chosen = picked.filter((a) => !blocked(a));
  const split = evenSplit(chosen);
  const pct = evenSplit(chosen, 100);
  const apy = split.reduce((s, w) => {
    const m = list.find((x) => x.asset === w.asset);
    return s + ((m?.best_apy ?? 0) * w.weight_bps) / TOTAL_BPS;
  }, 0);

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

  const toggle = (asset: string) =>
    setPicked((p) => (p.includes(asset) ? p.filter((a) => a !== asset) : [...p, asset]));

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
              <CountUp value={apy} format={(n) => fmtPct(n)} />
            </div>
          </div>
          <div className="w-px self-stretch bg-border" />
          <div className="flex-1">
            <div className="text-sm text-muted-foreground">Rewards / year</div>
            <div className="tnum mt-1 text-3xl font-medium tracking-tight">
              {amountValid && chosen.length > 0 ? fmtUsd((parsed * apy) / 100) : "—"}
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
            <Label>Tokens</Label>
            {assets.loading ? (
              <div className="mt-4">
                <Spinner label="Loading tokens…" />
              </div>
            ) : assets.error ? (
              <div className="mt-4">
                <ErrorBox message={assets.error} />
              </div>
            ) : list.length === 0 ? (
              <p className="mt-4 text-sm text-muted-foreground">
                No tokens indexed on {chain.name} yet.
              </p>
            ) : (
              <div className="mt-4">
                {many ? (
                  <MultiPicker
                    ariaLabel="Basket tokens"
                    values={picked}
                    onToggle={toggle}
                    disabled={locked}
                    options={list.map((a) => ({
                      value: a.asset,
                      label: displayAsset(a.asset),
                      hint: fmtPct(a.best_apy),
                      icon: <TokenIcon symbol={a.asset} size={18} />,
                      disabled: blocked(a.asset),
                      reason: noRoute,
                    }))}
                  />
                ) : (
                  <div className="flex flex-wrap gap-2">
                    {list.map((a) => {
                      const on = picked.includes(a.asset);
                      const off = blocked(a.asset);
                      const chip = (
                        <button
                          type="button"
                          onClick={() => !off && !locked && toggle(a.asset)}
                          aria-pressed={on}
                          aria-disabled={off || locked}
                          className={`inline-flex items-center gap-2 rounded-full border px-3 py-2 text-sm transition-colors ${
                            off
                              ? "cursor-not-allowed border-border opacity-40"
                              : on
                                ? "border-foreground bg-secondary"
                                : "border-border hover:bg-secondary/50"
                          }`}
                        >
                          <TokenIcon symbol={a.asset} size={20} />
                          <span className="font-medium">{displayAsset(a.asset)}</span>
                          <span className="tnum text-xs text-positive">
                            {fmtPct(a.best_apy)}
                          </span>
                        </button>
                      );
                      return off ? (
                        <Tooltip key={a.asset} label={noRoute}>
                          {chip}
                        </Tooltip>
                      ) : (
                        <span key={a.asset}>{chip}</span>
                      );
                    })}
                  </div>
                )}
              </div>
            )}

            {swaps.error && (
              <p className="mt-3 text-xs text-warning">
                could not read the swap allowlist ({swaps.error}) — every token is
                left selectable, so a missing route will surface at deposit
                instead of here
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
