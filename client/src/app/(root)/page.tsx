"use client";

import { useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import {
  QUOTE_ASSET,
  evenSplit,
  assetGroups,
  assetName,
  assetWarning,
  blendApy,
  displayAsset,
  displayProject,
  fmtPct,
  fmtUsd,
  fmtUsdOrUnknown,
  groupOf,
  legLabel,
  marketAssets,
  marketVenues,
  planFlowLegs,
  reachableAssets,
  reachableVenues,
  remapSelection,
  swapPaths,
  swappableAssets,
  venueProject,
  venueWarning,
  venuesById,
  weightsFor,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import {
  Button,
  Divider,
  ErrorBox,
  Label,
  Panel,
  Skeleton,
} from "@/components/ui";
import { Faq } from "@/components/Faq";
import { TokenPicker, type PickOption } from "@/components/TokenPicker";
import {
  SettleDetail,
  toastError,
  useSettleToast,
} from "@/components/SettleToast";
import { TokenIcon } from "@/components/TokenIcon";
import { BasketFlow } from "@/components/basket/BasketFlow";
import { CountUp, Reveal } from "@/components/motion";
import { cn } from "@/lib/utils";
import {
  TOTAL_BPS,
  type Basket,
  type FlowLeg,
  type Plan,
  type Portfolio,
  type SettleResult,
} from "@/lib/types";

type Phase = "idle" | "creating" | "funding" | "funded";
type Mode = "auto" | "manual";

type Choice = PickOption & { venueId?: string; family?: boolean };

export default function Create() {
  const {
    ready,
    authenticated,
    wallet,
    me,
    login,
    createWallet,
    delegate,
    api,
  } = useSession();
  const chain = useChain();
  const router = useRouter();

  const [name, setName] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const [custom, setCustom] = useState<{
    key: string;
    bps: Record<string, number>;
  } | null>(null);
  const [typing, setTyping] = useState<{ i: number; text: string } | null>(
    null,
  );
  const [mode, setMode] = useState<Mode>("auto");
  const [dropped, setDropped] = useState<string[]>([]);
  const [amount, setAmount] = useState("");
  const [phase, setPhase] = useState<Phase>("idle");
  const [created, setCreated] = useState<Basket | null>(null);
  const [fundFailed, setFundFailed] = useState(false);
  const { notify, detail, clear } = useSettleToast();

  const assets = useAsync(`assets:${chain.label}`, () => marketAssets());
  const swaps = useAsync(`swap-paths:${chain.label}`, swapPaths);
  const swappable = swaps.data
    ? swappableAssets(swaps.data, chain.chainId)
    : null;
  // Manual only: the summary now carries the split rate, so warnings need no join.
  const venues = useAsync(`venues:${chain.label}`, () => marketVenues());
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

  const autoChoices: Choice[] = groups.map((g) => {
    const a = g.best;
    const sym = g.family ? g.id : displayAsset(a.asset);
    return {
      id: g.id,
      asset: a.asset,
      family: g.family,
      symbol: sym,
      title: assetName(sym),
      sub: g.family
        ? `${sym} · via ${displayAsset(a.asset)}`
        : displayAsset(a.asset),
      apy: a.best_apy,
      tvl: g.members.reduce((t, m) => t + m.total_tvl_usd, 0),
      disabled: false,
      reason: "",
      warning: assetWarning(a) ?? undefined,
    };
  });

  const taken = new Map<string, string>();
  if (mode === "manual") {
    for (const id of picked) {
      const v = byVenue?.get(id);
      if (v && !taken.has(v.asset)) taken.set(v.asset, id);
    }
  }
  const manualChoices: Choice[] = reachableVenues(
    venues.data?.venues ?? [],
    swappable,
  )
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

  const options = mode === "auto" ? autoChoices : manualChoices;
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
  const settling = mode === "manual" ? venues.loading : assets.loading;
  const orphans = settling
    ? []
    : picked.filter((id) => !options.some((o) => o.id === id));
  // Pinned legs are the ones the user typed; everything else splits what is
  // left. Pinning never moves another pinned leg, so 400/400/200 is reachable.
  const selectionKey = chosen.map((o) => o.id).join(",");
  const pins = custom?.key === selectionKey ? custom.bps : null;
  const mix = pins ? "custom" : "equal";

  const split = useMemo(() => {
    const base = weightsFor(chosen);
    if (!pins) return base;
    const free = base.filter((_, j) => pins[chosen[j].id] === undefined);
    const taken = base.reduce((t, _, j) => t + (pins[chosen[j].id] ?? 0), 0);
    const share = evenSplit(
      free.map((w) => w.asset),
      Math.max(0, TOTAL_BPS - taken),
    );
    let k = 0;
    return base.map((w, j) => ({
      ...w,
      weight_bps: pins[chosen[j].id] ?? share[k++]?.weight_bps ?? 0,
    }));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectionKey, pins]);

  const totalBps = split.reduce((t, w) => t + w.weight_bps, 0);
  const balanced = totalBps === TOTAL_BPS;
  const isPinned = (i: number) => pins?.[chosen[i].id] !== undefined;

  function setMixMode(next: "equal" | "custom") {
    setTyping(null);
    setCustom(next === "equal" ? null : { key: selectionKey, bps: {} });
  }

  function pinLegBps(i: number, value: number) {
    const others = chosen.reduce(
      (t, o, j) => t + (j === i ? 0 : (pins?.[o.id] ?? 0)),
      0,
    );
    const capped = Math.min(
      Math.max(0, Math.round(value)),
      Math.max(0, TOTAL_BPS - others),
    );
    setCustom({
      key: selectionKey,
      bps: { ...(pins ?? {}), [chosen[i].id]: capped },
    });
  }

  function unpinLeg(i: number) {
    if (!pins) return;
    const next = { ...pins };
    delete next[chosen[i].id];
    setTyping(null);
    setCustom({ key: selectionKey, bps: next });
  }

  function setLegUsd(i: number, text: string) {
    setTyping({ i, text });
    const usd = Number(text);
    if (text.trim() === "" || !Number.isFinite(usd) || !amountValid) return;
    pinLegBps(i, (usd / parsed) * TOTAL_BPS);
  }

  const apy = blendApy(
    split,
    split.map((w, i) => ({ asset: w.asset, best_apy: chosen[i].apy })),
  );

  // Venue rows may still be loading, so a mapped id is verified against the
  // rendered options below rather than here.
  function switchMode(next: Mode) {
    if (next === mode) return;
    const toId =
      next === "manual"
        ? (id: string) =>
            groups.find((x) => x.id === id)?.best.best_venue ?? null
        : (id: string) =>
            groupOf(byVenue?.get(id)?.asset ?? "", groups)?.id ?? null;
    const res = remapSelection(picked, toId);
    setDropped(res.dropped.map(labelOf));
    setPicked(res.kept);
    setMode(next);
  }

  const busy = phase === "creating" || phase === "funding";
  const canSubmit =
    name.trim() !== "" && chosen.length > 0 && amountValid && balanced;
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

  // A basket nobody funded is a draft, not a basket. The server refuses the
  // delete once a leg has landed, so a partial fill keeps it.
  async function discard(id: string) {
    try {
      await api(`/v1/baskets/${id}`, { method: "DELETE" });
      setCreated(null);
    } catch {
      // keep it: the server says something is already in it
    }
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
      const filled = (res.data.legs ?? []).some(
        (l) => l.status === "submitted" || l.status === "pending",
      );
      notify(res.status, res.data, { key: id, verb: "Deposited" });
      portfolio.reload();
      if (!filled) {
        await discard(id);
        setFundFailed(true);
        setPhase("idle");
        return;
      }
      setPhase("funded");
      if (res.status !== 207) router.push(`/baskets/${id}`);
    } catch (e) {
      toastError(e);
      await discard(id);
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

  const preview: FlowLeg[] = split.map((w, i) => {
    const o = chosen[i];
    const v = o.venueId ? byVenue?.get(o.venueId) : undefined;
    const a = v ? undefined : list.find((x) => x.asset === o.asset);
    const id = v?.id ?? a?.best_venue;
    return {
      asset: o.asset,
      amountUsd: amountValid ? (parsed * w.weight_bps) / TOTAL_BPS : null,
      venue: id && o.apy > 0 ? { project: venueProject(id), apy: o.apy } : null,
      idle: false,
      family: o.family ? o.id : undefined,
      reason: o.warning ?? "",
    };
  });
  const legs = plan.data ? planFlowLegs(plan.data.legs) : preview;
  const warnings = new Map(chosen.map((o) => [o.asset, o.warning]));

  return (
    <main className="w-full px-4 pb-24 pt-8">
      <Reveal
        className={cn(
          "mx-auto w-full space-y-6 transition-[max-width] duration-500 ease-out",
          legs.length > 0 ? "max-w-5xl" : "max-w-xl",
        )}
      >
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

        <div
          className={cn(
            "grid gap-6 lg:items-start",
            legs.length > 0 && "lg:grid-cols-2",
          )}
        >
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
                <Skeleton className="mt-4 h-[42px] w-full rounded-lg" />
              ) : assets.error ? (
                <div className="mt-4">
                  <ErrorBox message={assets.error} />
                </div>
              ) : options.length === 0 && !settling ? (
                <p className="mt-4 text-sm text-muted-foreground">
                  {mode === "manual"
                    ? (venues.error ??
                      `No venues indexed on ${chain.name} yet.`)
                    : `No tokens indexed on ${chain.name} yet.`}
                </p>
              ) : (
                <Reveal className="mt-4" y={4}>
                  <TokenPicker
                    heading={
                      mode === "auto" ? "Select tokens" : "Select venues"
                    }
                    placeholder={
                      mode === "auto" ? "Select tokens" : "Select venues"
                    }
                    options={options}
                    tiles={tiles}
                    values={picked}
                    onToggle={toggle}
                    disabled={locked}
                    mode={mode}
                    onModeChange={
                      locked
                        ? undefined
                        : () => switchMode(mode === "auto" ? "manual" : "auto")
                    }
                  />
                  {chosen.length > 0 && (
                    <div className="mt-3 flex flex-wrap items-center gap-2">
                      {chosen.map((o) => (
                        <TokenIcon key={o.id} symbol={o.symbol} size={24} />
                      ))}
                    </div>
                  )}
                </Reveal>
              )}

              {chosen.some((o) => o.family) && (
                <p className="mt-3 text-xs text-muted-foreground">
                  the instrument shown is fixed at deposit and not re-picked
                  afterwards
                </p>
              )}

              {(dropped.length > 0 || orphans.length > 0) && (
                <p className="mt-3 text-xs text-warning">
                  not carried over to {mode} mode:{" "}
                  {[...dropped, ...orphans.map(labelOf)].join(", ")} — no
                  reachable {mode === "auto" ? "family" : "venue"} matches,
                  reselect if you want them
                </p>
              )}

              {swaps.error && (
                <p className="mt-3 text-xs text-warning">
                  could not read the swap allowlist ({swaps.error}) — nothing is
                  hidden, so a token with no route will surface at deposit
                  instead of here
                </p>
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
                  placeholder="1000"
                  className="tnum w-full bg-transparent text-4xl font-medium tracking-tight outline-none placeholder:text-muted-foreground/40"
                />
                <span className="inline-flex shrink-0 items-center gap-2 rounded-full bg-secondary/70 py-2 pl-3 pr-4 text-sm font-medium">
                  <TokenIcon symbol={QUOTE_ASSET} size={20} />
                  {QUOTE_ASSET}
                </span>
              </div>
            </div>

            <div className="p-5 pt-0">
              <Button
                className="w-full py-3"
                pending={busy}
                disabled={
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

          {legs.length > 0 && (
            <Reveal>
              <Panel>
                <div className="flex items-center justify-between px-5 pt-5">
                  <Label>Routing</Label>
                  <span className="text-xs text-muted-foreground">
                    {mode === "manual" ? "Manual" : "Auto"}
                  </span>
                </div>

                <BasketFlow legs={legs} swapMark />

                <div className="space-y-2 px-5 pb-5">
                  {!locked && chosen.length > 1 && (
                    <div className="flex items-center gap-1 pb-1">
                      {(["equal", "custom"] as const).map((m) => (
                        <button
                          key={m}
                          type="button"
                          onClick={() => setMixMode(m)}
                          className={cn(
                            "rounded-lg px-2.5 py-1 text-xs capitalize transition-colors",
                            mix === m
                              ? "bg-secondary text-foreground"
                              : "text-muted-foreground hover:text-foreground",
                          )}
                        >
                          {m}
                        </button>
                      ))}
                      {!balanced && amountValid && (
                        <span className="tnum ml-auto text-xs text-warning">
                          {fmtUsd(
                            (parsed * (TOTAL_BPS - totalBps)) / TOTAL_BPS,
                          )}{" "}
                          unassigned
                        </span>
                      )}
                    </div>
                  )}
                  {legs.map((l, i) => {
                    const warn =
                      warnings.get(l.asset) ??
                      (l.amountUsd === null
                        ? l.reason || "value unknown"
                        : !l.venue && !l.hold
                          ? l.reason || "no venue"
                          : null);
                    return (
                      <Reveal
                        key={l.asset}
                        delay={0.18 + i * 0.05}
                        y={6}
                        className={cn(
                          "rounded-xl border p-3",
                          warn
                            ? "border-destructive/25 bg-destructive/5"
                            : "border-border bg-secondary/30",
                        )}
                      >
                        <div className="flex items-center gap-2.5">
                          <TokenIcon symbol={l.family ?? l.asset} size={22} />
                          <span className="truncate text-sm font-medium">
                            {legLabel(l)}
                          </span>
                          {mix === "custom" && !locked && amountValid ? (
                            <span className="ml-auto flex items-center gap-1 text-sm">
                              <span className="text-muted-foreground">$</span>
                              <input
                                inputMode="decimal"
                                value={
                                  typing?.i === i
                                    ? typing.text
                                    : (
                                        (parsed * split[i].weight_bps) /
                                        TOTAL_BPS
                                      ).toFixed(2)
                                }
                                onChange={(e) => setLegUsd(i, e.target.value)}
                                onBlur={() => setTyping(null)}
                                aria-label={`${legLabel(l)} amount in ${QUOTE_ASSET}`}
                                className="tnum w-20 rounded-md border border-transparent bg-transparent px-1 py-0.5 text-right outline-none focus:border-border focus:bg-background"
                              />
                            </span>
                          ) : (
                            <span className="tnum ml-auto text-sm">
                              {l.amountUsd === null && !l.reason
                                ? "—"
                                : fmtUsdOrUnknown(l.amountUsd)}
                            </span>
                          )}
                        </div>
                        <div className="mt-1.5 flex items-center gap-3 pl-[30px] text-xs">
                          <span className="truncate text-muted-foreground">
                            {l.venue
                              ? displayProject(l.venue.project)
                              : l.hold
                                ? "your wallet"
                                : "not routed"}
                          </span>
                          <span
                            className={cn(
                              "tnum ml-auto",
                              l.venue
                                ? "text-positive"
                                : "text-muted-foreground",
                            )}
                          >
                            {l.venue
                              ? fmtPct(l.venue.apy)
                              : l.hold
                                ? fmtPct(0)
                                : "—"}
                          </span>
                        </div>
                        {mix === "custom" && !locked && split[i] && (
                          <div className="mt-2 flex items-center gap-2 pl-[30px] text-xs">
                            <span className="tnum text-muted-foreground">
                              {(split[i].weight_bps / 100).toFixed(0)}%
                            </span>
                            {isPinned(i) ? (
                              <button
                                type="button"
                                onClick={() => unpinLeg(i)}
                                className="text-muted-foreground underline underline-offset-2 transition-colors hover:text-foreground"
                              >
                                auto
                              </button>
                            ) : (
                              <span className="text-muted-foreground/60">
                                splits the rest
                              </span>
                            )}
                          </div>
                        )}
                        {warn && (
                          <p className="mt-2 text-xs text-destructive/80">
                            {warn}
                          </p>
                        )}
                      </Reveal>
                    );
                  })}
                  {plan.error && (
                    <p className="text-xs text-warning">{plan.error}</p>
                  )}
                </div>
              </Panel>
            </Reveal>
          )}
        </div>

        {created && fundFailed && (
          <div className="space-y-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-warning">
            <p>
              “{created.name}” kept something from an earlier attempt, so it was
              not discarded. Retry funds this same basket; it will not create
              another.
            </p>
            <div className="flex gap-4">
              <Link
                href={`/baskets/${created.id}`}
                className="underline underline-offset-2"
              >
                Open the basket
              </Link>
              <button
                type="button"
                onClick={reset}
                className="underline underline-offset-2"
              >
                Start a different basket
              </button>
            </div>
          </div>
        )}

        <Reveal delay={0.1}>
          <Faq />
        </Reveal>

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
