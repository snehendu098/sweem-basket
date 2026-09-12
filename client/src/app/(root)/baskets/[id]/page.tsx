"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ArrowLeft } from "lucide-react";
import {
  displayAsset,
  fmtBps,
  fmtPct,
  fmtUsd,
  market,
  publicBasket,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import {
  Button,
  Divider,
  ErrorBox,
  Label,
  Panel,
  Picker,
  SettleReport,
  Spinner,
} from "@/components/ui";
import { TokenIcon } from "@/components/TokenIcon";
import { CountUp, Reveal, Segmented } from "@/components/motion";
import type {
  Basket,
  BasketSummary,
  Plan,
  Portfolio,
  SettleResult,
} from "@/lib/types";

type Mode = "deposit" | "withdraw";

export default function BasketPage() {
  const { id } = useParams<{ id: string }>();
  const { ready, authenticated, wallet, me, login, createWallet, delegate, api } =
    useSession();

  const [mode, setMode] = useState<Mode>("deposit");
  const [amount, setAmount] = useState("1000");
  const [asset, setAsset] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [subBusy, setSubBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<{
    status: number;
    data: SettleResult;
  } | null>(null);

  const chain = useChain();
  const assets = useAsync(`assets:${chain.label}`, () => market.assets());
  // Anyone can look at a basket. The authed route is only used once there is a
  // session, because it is the one that knows whether you are subscribed.
  const anon = useAsync<BasketSummary | null>(
    `public-basket:${id}:${authenticated}`,
    () => (authenticated ? Promise.resolve(null) : publicBasket(id)),
  );
  const authedBasket = useApi<Basket>(authenticated ? `/v1/baskets/${id}` : null);
  const basket = authenticated ? authedBasket : anon;
  const portfolio = useApi<Portfolio>("/v1/portfolio");

  const parsed = Number(amount);
  const amountValid = Number.isFinite(parsed) && parsed > 0;

  // Planning on every keystroke would hammer the router; settle first.
  const [debounced, setDebounced] = useState(0);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(amountValid ? parsed : 0), 350);
    return () => clearTimeout(t);
  }, [parsed, amountValid]);

  const list = useMemo(() => assets.data?.assets ?? [], [assets.data]);
  // No hardcoded ticker: the tradeable set comes from market-data.
  const source = asset ?? list[0]?.asset ?? null;
  const balance = (portfolio.data?.onchain?.balances ?? []).find(
    (b) => b.symbol === source,
  );

  const b: BasketSummary | null = basket.data;
  const plan = useApi<Plan>(
    mode === "deposit" && debounced > 0
      ? `/v1/baskets/${id}/plan?amount_usd=${debounced}`
      : null,
  );
  const legs = plan.data?.legs ?? [];
  const apy = plan.data?.blended_apy ?? 0;
  const yearly = amountValid ? (parsed * apy) / 100 : 0;

  // Money held in this basket, the ceiling on a withdrawal.
  const held = (portfolio.data?.positions ?? [])
    .filter((p) => p.basket_id === id)
    .reduce((s, p) => s + p.amount_usd, 0);

  // One button, one next step. Onboarding has no page of its own.
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

  async function gate() {
    if (step === "connect") return login();
    if (step === "wallet") return createWallet();
    if (step === "delegate") return delegate();
  }

  async function toggleSubscription() {
    setSubBusy(true);
    setErr(null);
    try {
      await api(`/v1/baskets/${id}/subscribe`, {
        method: b?.subscribed ? "DELETE" : "POST",
      });
      authedBasket.reload();
    } catch (e) {
      // 412 carries the precondition that is missing; show it verbatim.
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSubBusy(false);
    }
  }

  async function settle() {
    setBusy(true);
    setErr(null);
    setResult(null);
    try {
      const res = await api<SettleResult>(`/v1/baskets/${id}/${mode}`, {
        method: "POST",
        body: JSON.stringify({ amount_usd: parsed }),
      });
      setResult({ status: res.status, data: res.data });
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
      portfolio.reload();
    }
  }

  return (
    <main className="w-full px-4 pb-24 pt-8">
      <Reveal className="mx-auto w-full max-w-lg space-y-6">
        <Link
          href="/invest"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          All baskets
        </Link>

        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <h1 className="truncate text-2xl font-medium tracking-tight">
              {b?.name ?? (basket.loading ? "…" : "Basket")}
            </h1>
            <p className="truncate text-sm text-muted-foreground">
              {(b?.weights ?? []).map((w) => displayAsset(w.asset)).join(" · ") || "—"}
            </p>
          </div>
          {b && authenticated && (
            <Button
              variant="ghost"
              disabled={subBusy}
              onClick={() => void toggleSubscription()}
            >
              {subBusy ? "Working…" : b.subscribed ? "Exit basket" : "Subscribe"}
            </Button>
          )}
        </div>

        {basket.error && <ErrorBox message={basket.error} />}

        <Segmented
          id="mode"
          value={mode}
          onChange={(m) => {
            setMode(m);
            setResult(null);
            setErr(null);
          }}
          options={[
            { value: "deposit", label: "Deposit" },
            { value: "withdraw", label: "Withdraw" },
          ]}
        />

        {/* Plain figures, one hairline between them. Not two bordered cards. */}
        <div className="flex items-stretch gap-6 px-1">
          <Figure
            label={mode === "deposit" ? "APY" : "Held in basket"}
            value={
              mode === "withdraw" ? (
                fmtUsd(held)
              ) : plan.data ? (
                <CountUp value={apy} format={(n) => fmtPct(n)} />
              ) : (
                "—"
              )
            }
            tone={mode === "deposit" ? "good" : undefined}
          />
          <div className="w-px self-stretch bg-border" />
          <Figure
            label={mode === "deposit" ? "Rewards / year" : "Blended APY"}
            value={
              mode === "withdraw"
                ? portfolio.data
                  ? fmtPct(portfolio.data.blended_apy)
                  : "—"
                : plan.data && amountValid
                  ? fmtUsd(yearly)
                  : "—"
            }
          />
        </div>

        <Panel>
          <div className="p-5">
            <div className="flex items-center justify-between">
              <Label>{mode === "deposit" ? "Amount in" : "Amount out"}</Label>
              {mode === "deposit"
                ? balance && (
                    <span className="tnum text-sm text-muted-foreground">
                      {balance.value} {balance.symbol}
                    </span>
                  )
                : held > 0 && (
                    <button
                      onClick={() => setAmount(String(held))}
                      className="text-sm text-muted-foreground transition-colors hover:text-foreground"
                    >
                      Max {fmtUsd(held)}
                    </button>
                  )}
            </div>
            <div className="mt-4 flex items-center gap-3">
              <input
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
                inputMode="decimal"
                aria-label="Amount in USD"
                placeholder="0.00"
                className="tnum w-full bg-transparent text-4xl font-medium tracking-tight outline-none placeholder:text-muted-foreground/40"
              />
              {mode === "deposit" && (
                <Picker
                  ariaLabel="Asset"
                  value={source}
                  onChange={setAsset}
                  placeholder={assets.loading ? "…" : "none"}
                  options={list.map((a) => ({
                    value: a.asset,
                    label: displayAsset(a.asset),
                    hint: fmtPct(a.best_apy),
                  }))}
                />
              )}
            </div>
            {assets.error && (
              <p className="mt-3 text-xs text-destructive">{assets.error}</p>
            )}
          </div>

          {mode === "deposit" && (
            <>
              <Divider />
              <div className="p-5">
                <Label>Routing</Label>
                {!authenticated ? (
                  <p className="mt-4 text-sm text-muted-foreground">
                    Connect to see where each slice would be routed.
                  </p>
                ) : plan.loading ? (
                  <div className="mt-4">
                    <Spinner label="Planning…" />
                  </div>
                ) : plan.error ? (
                  <div className="mt-4">
                    <ErrorBox message={plan.error} />
                  </div>
                ) : (
                  <ul className="mt-4 space-y-3">
                    {legs.map((l) => (
                      <li key={l.asset} className="text-sm">
                        <div className="flex items-center gap-3">
                          <span className="inline-flex items-center gap-2">
                          <TokenIcon symbol={l.asset} size={18} />
                          {displayAsset(l.asset)}
                        </span>
                          <span className="tnum text-xs text-muted-foreground">
                            {fmtBps(l.weight_bps)}
                          </span>
                          <span className="tnum ml-auto">
                            {fmtUsd(l.amount_usd)}
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
                )}
              </div>
            </>
          )}

          <Divider />

          <dl className="space-y-2 px-5 py-4 text-sm">
            <Row label="Network" value={b?.chain ?? chain.name} />
            <Row label="Fee" value={b ? fmtBps(b.fee_bps) : "—"} />
          </dl>

          <div className="p-5 pt-0">
            <Button
              className="w-full py-3"
              disabled={
                busy ||
                step === "loading" ||
                step === "syncing" ||
                (step === "ready" && (!amountValid || !b))
              }
              onClick={() => void (step === "ready" ? settle() : gate())}
            >
              {busy
                ? "Executing…"
                : step === "connect"
                  ? "Connect"
                  : step === "wallet"
                    ? "Create wallet"
                    : step === "delegate"
                      ? "Enable delegation"
                      : step === "ready"
                        ? `${mode === "deposit" ? "Deposit" : "Withdraw"} ${
                            amountValid ? fmtUsd(parsed) : ""
                          }`
                        : "…"}
            </Button>
          </div>
        </Panel>

        {err && <ErrorBox message={err} />}
        {result && <SettleReport result={result.data} status={result.status} />}
      </Reveal>
    </main>
  );
}

function Figure({
  label,
  value,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  tone?: "good";
}) {
  return (
    <div className="flex-1">
      <div className="text-sm text-muted-foreground">{label}</div>
      <div
        className={`tnum mt-1 text-3xl font-medium tracking-tight ${
          tone === "good" ? "text-positive" : "text-foreground"
        }`}
      >
        {value}
      </div>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="tnum truncate">{value}</dd>
    </div>
  );
}
