"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { basescanTx, fmtUsd, shortHash } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { LegResult, LegStatus, SettleResult, Step } from "@/lib/types";

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center gap-2 text-sm text-zinc-500">
      <span className="h-3 w-3 animate-spin rounded-full border-2 border-zinc-700 border-t-zinc-300" />
      {label ?? "Loading…"}
    </div>
  );
}

export function ErrorBox({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div className="rounded border border-red-900/60 bg-red-950/30 p-3 text-sm text-red-300">
      <div className="font-medium">Request failed</div>
      <div className="mt-1 break-words text-red-400/90">{message}</div>
      {onRetry && (
        <button
          onClick={onRetry}
          className="mt-2 rounded border border-red-800 px-2 py-1 text-xs text-red-200 hover:bg-red-900/40"
        >
          Retry
        </button>
      )}
    </div>
  );
}

export function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-500">
      {children}
    </div>
  );
}

export function Panel({
  title,
  right,
  children,
  className = "",
}: {
  title?: string;
  right?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section
      className={`rounded-lg border border-zinc-800 bg-zinc-900/40 ${className}`}
    >
      {title && (
        <header className="flex items-center justify-between border-b border-zinc-800 px-4 py-2.5">
          <h2 className="text-xs font-semibold uppercase tracking-wider text-zinc-400">
            {title}
          </h2>
          {right}
        </header>
      )}
      <div className="p-4">{children}</div>
    </section>
  );
}

export function Stat({
  label,
  value,
  sub,
  tone = "default",
}: {
  label: string;
  value: string;
  sub?: string;
  tone?: "default" | "good" | "warn";
}) {
  const color =
    tone === "good"
      ? "text-emerald-400"
      : tone === "warn"
        ? "text-amber-400"
        : "text-zinc-100";
  return (
    <div>
      <div className="text-[11px] uppercase tracking-wider text-zinc-500">
        {label}
      </div>
      <div className={`mt-1 font-mono text-xl ${color}`}>{value}</div>
      {sub && <div className="mt-0.5 text-xs text-zinc-500">{sub}</div>}
    </div>
  );
}

export function Button({
  children,
  variant = "primary",
  className = "",
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "primary" | "ghost" | "danger";
}) {
  const styles = {
    primary:
      "bg-emerald-500 text-zinc-950 hover:bg-emerald-400 disabled:bg-zinc-700 disabled:text-zinc-400",
    ghost:
      "border border-zinc-700 text-zinc-200 hover:bg-zinc-800 disabled:text-zinc-600",
    danger:
      "border border-red-800 text-red-300 hover:bg-red-950/50 disabled:text-zinc-600",
  }[variant];
  return (
    <button
      {...rest}
      className={`rounded px-3 py-1.5 text-sm font-medium transition disabled:cursor-not-allowed ${styles} ${className}`}
    >
      {children}
    </button>
  );
}

const NAV = [
  { href: "/explore", label: "Explore" },
  { href: "/create", label: "Create" },
  { href: "/portfolio", label: "Portfolio" },
  { href: "/activity", label: "Activity" },
];

export function Nav() {
  const path = usePathname();
  const { ready, authenticated, login, logout, me, wallet } = useSession();

  return (
    <header className="sticky top-0 z-20 border-b border-zinc-800 bg-zinc-950/90 backdrop-blur">
      <div className="mx-auto flex max-w-6xl items-center gap-6 px-5 py-3">
        <Link href="/" className="font-semibold tracking-tight">
          <span className="text-emerald-400">▮</span> Basket
        </Link>
        {authenticated && (
          <nav className="flex gap-4 text-sm">
            {NAV.map((n) => (
              <Link
                key={n.href}
                href={n.href}
                className={
                  path === n.href || path.startsWith(n.href + "/")
                    ? "text-zinc-100"
                    : "text-zinc-500 hover:text-zinc-300"
                }
              >
                {n.label}
              </Link>
            ))}
          </nav>
        )}
        <div className="ml-auto flex items-center gap-3 text-sm">
          {!ready ? (
            <Spinner label="" />
          ) : authenticated ? (
            <>
              <Link
                href="/onboarding"
                className={`rounded-full border px-2.5 py-1 text-xs ${
                  me?.delegated
                    ? "border-emerald-800 text-emerald-400"
                    : "border-amber-800 text-amber-400"
                }`}
                title="Manage the permission you granted"
              >
                {me?.delegated ? "Delegated · revoke" : "Not delegated"}
              </Link>
              {wallet && (
                <span className="hidden font-mono text-xs text-zinc-500 sm:inline">
                  {shortHash(wallet.address)}
                </span>
              )}
              <Button variant="ghost" onClick={() => void logout()}>
                Log out
              </Button>
            </>
          ) : (
            <Button onClick={login}>Log in</Button>
          )}
        </div>
      </div>
    </header>
  );
}

/** Gate for pages that need a logged-in user with a bound backend record. */
export function RequireAuth({ children }: { children: React.ReactNode }) {
  const { ready, authenticated, login, me, meError, syncing, refreshMe } =
    useSession();

  if (!ready) return <Spinner label="Starting Privy…" />;
  if (!authenticated) {
    return (
      <Panel title="Sign in required">
        <p className="text-sm text-zinc-400">
          Log in to create an embedded wallet and use the protocol.
        </p>
        <Button className="mt-3" onClick={login}>
          Log in
        </Button>
      </Panel>
    );
  }
  if (meError) return <ErrorBox message={meError} onRetry={() => void refreshMe()} />;
  if (!me || syncing) return <Spinner label="Binding your wallet…" />;
  return <>{children}</>;
}

// --- execution rendering: 207, pending, and null prices are first-class ---

const LEG_TONE: Record<LegStatus, string> = {
  submitted: "border-emerald-800 bg-emerald-950/40 text-emerald-300",
  pending: "border-amber-800 bg-amber-950/40 text-amber-300",
  failed: "border-red-800 bg-red-950/40 text-red-300",
  skipped: "border-zinc-700 bg-zinc-800/40 text-zinc-400",
};

export function StatusPill({ status }: { status: string }) {
  const tone =
    LEG_TONE[status as LegStatus] ??
    (status === "confirmed"
      ? LEG_TONE.submitted
      : status === "reverted"
        ? LEG_TONE.failed
        : LEG_TONE.skipped);
  return (
    <span
      className={`rounded border px-1.5 py-0.5 font-mono text-[11px] uppercase ${tone}`}
    >
      {status}
    </span>
  );
}

export function TxLink({ hash }: { hash: string }) {
  return (
    <a
      href={basescanTx(hash)}
      target="_blank"
      rel="noreferrer"
      className="font-mono text-xs text-sky-400 underline-offset-2 hover:underline"
    >
      {shortHash(hash)}
    </a>
  );
}

export function Steps({ steps }: { steps: Step[] }) {
  return (
    <ol className="mt-2 space-y-1 border-l border-zinc-800 pl-3">
      {steps.map((s) => (
        <li key={s.step} className="flex items-center gap-2 text-xs">
          <span className="text-zinc-500">step {s.step}</span>
          <StatusPill status={s.outcome} />
          {s.tx_hash && <TxLink hash={s.tx_hash} />}
        </li>
      ))}
    </ol>
  );
}

/**
 * Renders a deposit/rebalance response. HTTP 207 is the normal partial case:
 * show which legs moved and which did not, never one blanket failure.
 * `pending` means the receipt poll timed out — the transaction may still land.
 */
export function SettleReport({
  result,
  status,
}: {
  result: SettleResult;
  status: number;
}) {
  const legs = result.legs ?? [];
  return (
    <div className="space-y-3">
      <div
        className={`rounded border p-3 text-sm ${
          status === 207
            ? "border-amber-800 bg-amber-950/30 text-amber-200"
            : "border-emerald-800 bg-emerald-950/30 text-emerald-200"
        }`}
      >
        {status === 207 ? (
          <>
            <div className="font-medium">
              Partially settled ({result.failed_legs} failed,{" "}
              {result.pending_legs} pending)
            </div>
            <div className="mt-1 text-amber-300/80">
              Legs are independent. The ones marked submitted moved money; the
              rest are listed below with their reason.
            </div>
          </>
        ) : (
          <div className="font-medium">
            All legs settled
            {result.submitted_usd !== undefined &&
              ` · ${fmtUsd(result.submitted_usd)} submitted`}
            {result.moved_legs !== undefined && ` · ${result.moved_legs} moved`}
          </div>
        )}
      </div>
      {result.pending_legs > 0 && (
        <p className="text-xs text-amber-400/90">
          Pending is not a failure: the executor&apos;s receipt poll timed out
          and the transaction is probably still in the mempool. Check the tx
          hash on Basescan before retrying — retrying could submit the same
          money twice.
        </p>
      )}
      {legs.length === 0 ? (
        <Empty>No legs were produced for this request.</Empty>
      ) : (
        <ul className="divide-y divide-zinc-800 rounded border border-zinc-800">
          {legs.map((leg, i) => (
            <LegRow key={`${leg.asset}-${i}`} leg={leg} />
          ))}
        </ul>
      )}
    </div>
  );
}

function LegRow({ leg }: { leg: LegResult }) {
  return (
    <li className="p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="font-mono text-sm text-zinc-100">{leg.asset}</span>
        <StatusPill status={leg.status} />
        <span className="font-mono text-sm text-zinc-400">
          {fmtUsd(leg.amount_usd)}
        </span>
        {leg.project && (
          <span className="text-xs text-zinc-500">→ {leg.project}</span>
        )}
        {leg.apy !== undefined && leg.apy > 0 && (
          <span className="font-mono text-xs text-emerald-400">
            {leg.apy.toFixed(2)}% APY
          </span>
        )}
        {leg.tx_hash && <TxLink hash={leg.tx_hash} />}
      </div>
      {leg.venue_id && (
        <div className="mt-1 break-all font-mono text-[11px] text-zinc-600">
          {leg.from_venue_id ? `${leg.from_venue_id} → ` : ""}
          {leg.venue_id}
        </div>
      )}
      {leg.reason && (
        <div className="mt-1 text-xs text-zinc-400">{leg.reason}</div>
      )}
      {leg.steps && leg.steps.length > 0 && <Steps steps={leg.steps} />}
    </li>
  );
}
