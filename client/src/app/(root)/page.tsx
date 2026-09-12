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
  market,
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

/**
 * Creating a basket and funding it are one card and one button, but two calls:
 * POST /v1/baskets then POST /v1/baskets/{id}/deposit. There is no combined
 * endpoint, so the half-done state is real and named.
 *
 * `created` outlives a failed deposit on purpose — the basket exists, and the
 * retry deposits into that id rather than creating a second empty basket.
 */
type Phase =
  | { kind: "idle" }
  | { kind: "creating" }
  | { kind: "funding" }
  | { kind: "funded"; status: number; data: SettleResult };

export default function Create() {
  const { ready, authenticated, wallet, me, login, createWallet, delegate, api } =
    useSession();
  const chain = useChain();
  const router = useRouter();

  const [name, setName] = useState("");
  // Selection only. Basis points are a protocol unit, not something anyone
  // creating a basket thinks in — the split is even and computed at submit.
  const [picked, setPicked] = useState<string[]>([]);
  const [amount, setAmount] = useState("1000");
  const [phase, setPhase] = useState<Phase>({ kind: "idle" });
  /** Set once creation succeeds. Never cleared by a failed deposit. */
  const [created, setCreated] = useState<Basket | null>(null);
  /** Which of the two calls the error came from, so the card can say so. */
  const [failedAt, setFailedAt] = useState<"create" | "fund" | null>(null);
  const { notify, detail, clear } = useSettleToast();

  // The key carries the chain: switching networks is a different question,
  // not a stale answer.
  const assets = useAsync(`assets:${chain.label}`, () => market.assets());
  const list = assets.data?.assets ?? [];
  // The executor's swap allowlist, the only source of truth for what a USDC
  // deposit can be converted into. Null while loading or on failure.
  const swaps = useAsync(`swap-paths:${chain.label}`, swapPaths);
  const swappable = swaps.data
    ? swappableAssets(swaps.data, chain.chainId)
    : null;
  const portfolio = useApi<Portfolio>("/v1/portfolio");
  const balance = (portfolio.data?.onchain?.balances ?? []).find(
    (b) => b.symbol === QUOTE_ASSET,
  );

  // Once the basket exists the backend can price the real routing, prices and
  // all. Before that the preview is derived from the asset summary.
  const parsed = Number(amount);
  const amountValid = Number.isFinite(parsed) && parsed > 0;
  const plan = useApi<Plan>(
    created && amountValid
      ? `/v1/baskets/${created.id}/plan?amount_usd=${parsed}`
      : null,
  );

  // Switching networks can strand an already-picked token on a chain with no
  // route to it. Drop it here rather than shipping a basket that cannot fund.
  const chosen = picked.filter((a) => !blocked(a));
  const split = evenSplit(chosen);
  // Whole percents that also sum to 100, so 3 tokens reads 34/33/33 and not
  // three 33s. Same largest-remainder rule, different total.
  const pct = evenSplit(chosen, 100);
  const apy = split.reduce((s, w) => {
    const m = list.find((x) => x.asset === w.asset);
    return s + ((m?.best_apy ?? 0) * w.weight_bps) / TOTAL_BPS;
  }, 0);

  // A token a USDC deposit cannot acquire is not offered at all — the reason
  // lives in a tooltip, so it is answered on hover instead of discovered after
  // a failed deposit. One predicate, so GET /swaps replaces it in one place.
  const blocked = (asset: string) => !isSwappable(asset, swappable);
  const noRoute = `no USDC swap route on ${chain.name} — this token cannot be bought with your deposit here`;
  // Above a handful the chip row wraps into a wall; mainnet indexes a dozen.
  const many = list.length > 5;

  const busy = phase.kind === "creating" || phase.kind === "funding";
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
    setPhase({ kind: "idle" });
    clear();
    setFailedAt(null);
  }

  /** POST the deposit into an existing basket. The retry path is this, alone. */
  async function fund(id: string) {
    setPhase({ kind: "funding" });
    clear();
    setFailedAt(null);
    try {
      const res = await api<SettleResult>(`/v1/baskets/${id}/deposit`, {
        method: "POST",
        body: JSON.stringify({ amount_usd: parsed }),
      });
      // 207 is a result, not an error: the toast warns and keeps the leg by
      // leg breakdown one click away, and navigating away would hide which
      // legs failed or are still pending. A clean settle moves on.
      setPhase({ kind: "funded", status: res.status, data: res.data });
      notify(res.status, res.data, { key: id, verb: "Deposited" });
      portfolio.reload();
      if (res.status !== 207) router.push(`/baskets/${id}`);
    } catch (e) {
      // The basket still exists and is empty. Say so, and offer the retry that
      // deposits into it instead of creating another one.
      toastError(e);
      setFailedAt("fund");
      setPhase({ kind: "idle" });
      portfolio.reload();
    }
  }

  async function submit() {
    if (step === "connect") return login();
    if (step === "wallet") return createWallet();
    if (step === "delegate") return delegate();
    // After a settle — including a partial one — the money has already moved.
    // The next action is to go look at it, never to send the same amount again.
    if (phase.kind === "funded" && created) {
      router.push(`/baskets/${created.id}`);
      return;
    }
    if (created) return fund(created.id);

    setPhase({ kind: "creating" });
    clear();
    setFailedAt(null);
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
          // Real symbols, not display names: WETH stays WETH on the wire.
          weights: split,
        }),
      });
      basket = res.data;
    } catch (e) {
      toastError(e);
      setFailedAt("create");
      setPhase({ kind: "idle" });
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
                      // The chip reads as a token; the reason lives in the
                      // tooltip on the wrapper, which still hovers when the
                      // control inside it is disabled.
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
                {/* Read-only: the split is always even, so there is nothing to edit. */}
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
              {/* Not a picker. You deposit dollars; the tokens above are what
                  they are converted into. */}
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
                    {/* A missing price is never substituted with a number. */}
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
              {phase.kind === "creating"
                ? "Creating basket…"
                : phase.kind === "funding"
                  ? `Depositing ${amountValid ? fmtUsd(parsed) : ""}…`
                  : step === "connect"
                    ? "Connect"
                    : step === "wallet"
                      ? "Create wallet"
                      : step === "delegate"
                        ? "Enable delegation"
                        : step === "syncing" || step === "loading"
                          ? "…"
                          : phase.kind === "funded"
                            ? "Open basket"
                            : created
                              ? `Retry deposit${amountValid ? ` ${fmtUsd(parsed)}` : ""}`
                              : `Create & deposit${amountValid ? ` ${fmtUsd(parsed)}` : ""}`}
            </Button>
          </div>
        </Panel>

        {/* Created but not funded. The basket is real and empty; do not pretend
            otherwise, and do not create a second one on retry. */}
        {created && failedAt === "fund" && (
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
        {phase.kind === "funded" && created && (
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
