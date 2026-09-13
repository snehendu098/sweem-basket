"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useMemo, useState } from "react";
import { ArrowLeft, ExternalLink } from "lucide-react";
import {
  QUOTE_ASSET,
  basescanAddress,
  displayAsset,
  fmtBps,
  fmtPct,
  fmtTime,
  fmtUsd,
  planFlowLegs,
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
  Spinner,
} from "@/components/ui";
import { SettleDetail, toastError, useSettleToast } from "@/components/SettleToast";
import { TokenIcon } from "@/components/TokenIcon";
import { CountUp, Reveal, Segmented } from "@/components/motion";
import { BasketFlow } from "@/components/basket/BasketFlow";
import {
  HOLD_VENUE_ID,
  IDLE_VENUE_ID,
  type FlowLeg,
  type Basket,
  type BasketSummary,
  type Plan,
  type Portfolio,
  type SettleResult,
} from "@/lib/types";

type Mode = "deposit" | "withdraw";

export default function BasketPage() {
  const { id } = useParams<{ id: string }>();
  const { ready, authenticated, wallet, me, login, createWallet, delegate, api } =
    useSession();

  const [mode, setMode] = useState<Mode>("deposit");
  const [amount, setAmount] = useState("1000");
  const [busy, setBusy] = useState(false);
  const [subBusy, setSubBusy] = useState(false);
  const { notify, detail, clear } = useSettleToast();

  const chain = useChain();
  const anon = useAsync<BasketSummary | null>(
    `public-basket:${id}:${authenticated}`,
    () => (authenticated ? Promise.resolve(null) : publicBasket(id)),
  );
  const authedBasket = useApi<Basket>(authenticated ? `/v1/baskets/${id}` : null);
  const basket = authenticated ? authedBasket : anon;
  const portfolio = useApi<Portfolio>("/v1/portfolio");

  const parsed = Number(amount);
  const amountValid = Number.isFinite(parsed) && parsed > 0;

  const [debounced, setDebounced] = useState(0);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(amountValid ? parsed : 0), 350);
    return () => clearTimeout(t);
  }, [parsed, amountValid]);

  const b: BasketSummary | null = basket.data;
  const balance = (portfolio.data?.onchain?.balances ?? []).find(
    (x) => x.symbol === QUOTE_ASSET,
  );

  const plan = useApi<Plan>(
    debounced > 0 ? `/v1/baskets/${id}/plan?amount_usd=${debounced}` : null,
  );

  const holdings = useMemo(
    () => (portfolio.data?.positions ?? []).filter((p) => p.basket_id === id),
    [portfolio.data, id],
  );
  const held = holdings.reduce((s, p) => s + p.amount_usd, 0);

  const legs = useMemo((): FlowLeg[] => {
    if (holdings.length > 0) {
      return holdings.map((h) => {
        const idle = h.venue_id === IDLE_VENUE_ID;
        const hold = h.venue_id === HOLD_VENUE_ID;
        return {
          asset: h.asset,
          amountUsd: h.onchain_usd ?? h.amount_usd,
          venue:
            idle || hold ? null : { project: h.project, apy: h.current_apy },
          idle,
          hold,
          reason: h.value_reason,
        };
      });
    }
    return planFlowLegs(plan.data?.legs);
  }, [holdings, plan.data]);

  const apy =
    holdings.length > 0 && held > 0
      ? holdings.reduce((s, h) => s + (h.current_apy * h.amount_usd) / held, 0)
      : (plan.data?.blended_apy ?? null);
  const yearly = amountValid && apy !== null ? (parsed * apy) / 100 : null;

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
    try {
      await api(`/v1/baskets/${id}/subscribe`, {
        method: b?.subscribed ? "DELETE" : "POST",
      });
      authedBasket.reload();
    } catch (e) {
      toastError(e);
    } finally {
      setSubBusy(false);
    }
  }

  async function settle(all = false) {
    setBusy(true);
    clear();
    try {
      const res = await api<SettleResult>(`/v1/baskets/${id}/${mode}`, {
        method: "POST",
        body: JSON.stringify(all ? { all: true } : { amount_usd: parsed }),
      });
      const clean = notify(res.status, res.data, {
        key: id,
        verb: mode === "deposit" ? "Deposited" : "Withdrew",
      });
      if (clean) setAmount("");
    } catch (e) {
      toastError(e);
    } finally {
      setBusy(false);
      portfolio.reload();
    }
  }

  const weights = b?.weights ?? [];
  const handle = weights.map((w) => displayAsset(w.asset)).join(" · ");

  return (
    <main className="w-full px-4 pb-24 pt-8">
      <Reveal className="mx-auto w-full max-w-6xl space-y-6">
        <Link
          href="/invest"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground transition-colors hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
          All baskets
        </Link>

        <div className="flex flex-wrap items-center justify-between gap-4">
          <div className="flex min-w-0 items-center gap-3">
            <span aria-hidden className="flex shrink-0 -space-x-2">
              {weights.slice(0, 3).map((w) => (
                <TokenIcon key={w.asset} symbol={w.asset} size={36} />
              ))}
            </span>
            <div className="min-w-0">
              <h1 className="truncate text-2xl font-medium tracking-tight">
                {b?.name ?? (basket.loading ? "…" : "Basket")}
              </h1>
              <p className="truncate text-sm text-muted-foreground">
                {handle || "—"}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {b && authenticated && (
              <Button
                variant="ghost"
                disabled={subBusy}
                onClick={() => void toggleSubscription()}
              >
                {subBusy ? "Working…" : b.subscribed ? "Exit basket" : "Subscribe"}
              </Button>
            )}
            <a href="#fund">
              <Button onClick={() => setMode("deposit")}>Deposit</Button>
            </a>
          </div>
        </div>

        {basket.error && <ErrorBox message={basket.error} />}

        <div className="grid gap-6 lg:grid-cols-3">
          <div className="space-y-6 lg:col-span-2">
            <Panel>
              <div className="flex items-center justify-between px-5 pt-5">
                <Label>Where the funds are</Label>
                {plan.loading && holdings.length === 0 && <Spinner />}
              </div>
              <BasketFlow
                legs={legs}
                emptyLabel={
                  authenticated
                    ? "Nothing here yet. Enter an amount to see where it would be routed."
                    : "Connect to see where this basket routes."
                }
              />
            </Panel>

            <div className="grid gap-4 sm:grid-cols-3">
              <Stat
                label="APY"
                tone="good"
                value={apy === null ? "—" : <CountUp value={apy} format={fmtPct} />}
                note={
                  holdings.length > 0 ? "on your positions" : "on the routing plan"
                }
              />
              <Stat
                label="Your position"
                value={authenticated ? fmtUsd(held) : "—"}
                note="this basket, your wallet"
              />
              <Stat
                label="Creator"
                value={
                  b?.created_by_me === undefined
                    ? "—"
                    : b.created_by_me
                      ? "You"
                      : "Another user"
                }
                note={b?.created_at ? fmtTime(b.created_at) : undefined}
              />
            </div>

            <Panel>
              <div className="p-5">
                <Label>About</Label>
                <p className="mt-3 text-sm text-muted-foreground">
                  {b?.description?.trim() ||
                    "No description. The allocation above is the whole of it."}
                </p>
              </div>
              <Divider />
              <dl className="space-y-2 px-5 py-4 text-sm">
                <Row label="Network" value={b?.chain ?? chain.name} />
                <Row label="Fee" value={b ? fmtBps(b.fee_bps) : "—"} />
                <Row label="Basket id" value={b?.id ?? "—"} />
                <Row
                  label="Allocation"
                  value={
                    weights.map((w) => `${displayAsset(w.asset)} ${fmtBps(w.weight_bps)}`).join(
                      " · ",
                    ) || "—"
                  }
                />
              </dl>
              <Divider />
              <div className="p-5">
                <Label>Contracts</Label>
                <ul className="mt-3 space-y-2 text-sm">
                  <AddressRow symbol={QUOTE_ASSET} address={chain.usdc} />
                  {weights.map((w) => {
                    const bal = (portfolio.data?.onchain?.balances ?? []).find(
                      (x) => x.symbol === w.asset,
                    );
                    return bal ? (
                      <AddressRow
                        key={w.asset}
                        symbol={w.asset}
                        address={bal.contract}
                      />
                    ) : null;
                  })}
                  {portfolio.data?.wallet_address && (
                    <AddressRow
                      symbol="Your wallet"
                      address={portfolio.data.wallet_address}
                      plain
                    />
                  )}
                </ul>
              </div>
            </Panel>
          </div>

          <div id="fund" className="lg:sticky lg:top-24 lg:self-start">
            <Panel>
              <div className="p-5">
                <Segmented
                  id="mode"
                  value={mode}
                  onChange={(m) => {
                    setMode(m);
                    clear();
                  }}
                  options={[
                    { value: "deposit", label: "Deposit" },
                    { value: "withdraw", label: "Withdraw" },
                  ]}
                />
              </div>

              <Divider />

              <div className="p-5">
                <div className="flex items-center justify-between">
                  <Label>{mode === "deposit" ? "Amount in" : "Amount out"}</Label>
                  {mode === "deposit"
                    ? balance && (
                        <button
                          type="button"
                          onClick={() => setAmount(String(balance.value))}
                          className="tnum text-sm text-muted-foreground transition-colors hover:text-foreground"
                        >
                          {balance.value} {balance.symbol}
                        </button>
                      )
                    : held > 0 && (
                        <button
                          type="button"
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
                    aria-label="Amount in USDC"
                    placeholder="0.00"
                    className="tnum w-full bg-transparent text-4xl font-medium tracking-tight outline-none placeholder:text-muted-foreground/40"
                  />
                  <span className="inline-flex shrink-0 items-center gap-2 rounded-full bg-secondary/70 py-2 pl-3 pr-4 text-sm font-medium">
                    <TokenIcon symbol={QUOTE_ASSET} size={20} />
                    {QUOTE_ASSET}
                  </span>
                </div>
                {mode === "deposit" && yearly !== null && (
                  <p className="tnum mt-3 text-xs text-muted-foreground">
                    ≈ {fmtUsd(yearly)} / year at {apy === null ? "—" : fmtPct(apy)}
                  </p>
                )}
              </div>

              <Divider />

              <div className="space-y-3 p-5">
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
                {mode === "withdraw" && step === "ready" && held > 0 && (
                  <Button
                    variant="ghost"
                    className="w-full"
                    disabled={busy}
                    onClick={() => void settle(true)}
                  >
                    Withdraw everything
                  </Button>
                )}
                {plan.error && mode === "deposit" && (
                  <p className="text-xs text-warning">{plan.error}</p>
                )}
              </div>
            </Panel>

            {detail && (
              <div className="mt-4">
                <SettleDetail detail={detail} onClose={clear} />
              </div>
            )}
          </div>
        </div>
      </Reveal>
    </main>
  );
}

function Stat({
  label,
  value,
  note,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  note?: string;
  tone?: "good";
}) {
  return (
    <Panel className="p-5">
      <div className="text-sm text-muted-foreground">{label}</div>
      <div
        className={`tnum mt-1 truncate text-2xl font-medium tracking-tight ${
          tone === "good" ? "text-positive" : "text-foreground"
        }`}
      >
        {value}
      </div>
      {note && <div className="mt-1 text-xs text-muted-foreground">{note}</div>}
    </Panel>
  );
}

function AddressRow({
  symbol,
  address,
  plain,
}: {
  symbol: string;
  address: string;
  plain?: boolean;
}) {
  return (
    <li className="flex items-center justify-between gap-3">
      <span className="inline-flex items-center gap-2 text-muted-foreground">
        {!plain && <TokenIcon symbol={symbol} size={18} />}
        {plain ? symbol : displayAsset(symbol)}
      </span>
      <a
        href={basescanAddress(address)}
        target="_blank"
        rel="noreferrer"
        className="tnum inline-flex items-center gap-1 truncate text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
      >
        {address}
        <ExternalLink className="size-3 shrink-0" />
      </a>
    </li>
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
