"use client";

import Image from "next/image";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { Check, Copy, Settings } from "lucide-react";
import { motion, useReducedMotion } from "motion/react";
import {
  basescanAddress,
  fmtToken,
  readBalances,
  shortHash,
  type WalletBalances,
} from "@/lib/api";
import { useChain } from "@/lib/chain";
import { useApi, useAsync, useSession } from "@/lib/session";
import { cn } from "@/lib/utils";
import type { Portfolio } from "@/lib/types";
import { Spinner, useDismissable } from "./ui";

const NAV = [
  { label: "Create", href: "/" },
  { label: "Invest", href: "/invest" },
  { label: "Portfolio", href: "/portfolio" },
] as const;

/**
 * The header lays out its own children edge to edge, so the wordmark sits at
 * the left margin no matter how the page wrapper aligns the header itself.
 */
export function Navbar() {
  const pathname = usePathname();
  const reduced = useReducedMotion();
  const { ready, authenticated, login } = useSession();

  return (
    <header className="w-full">
      <div className="w-full flex items-center justify-between px-10 py-5">
        <div className="flex items-center gap-1">
          <Link
            href="/"
            className="mr-6 flex items-center gap-2 text-lg font-semibold tracking-tight"
          >
            {/* Above the fold on every route, so it is not lazy-loaded.
                A 64px source served as-is: at 26px there is nothing for the
                optimizer to save, and unoptimized keeps the standalone runtime
                from needing sharp to render the wordmark. */}
            <Image
              src="/sweem-mark.png"
              alt=""
              width={26}
              height={26}
              priority
              unoptimized
              className="size-[26px]"
            />
            sweem
          </Link>
          <nav className="flex items-center gap-0.5">
            {NAV.map((item) => {
              const active = pathname === item.href;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={cn(
                    "relative rounded-full px-5 py-2.5 text-sm font-medium transition-colors",
                    active
                      ? "text-foreground"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {active &&
                    (reduced ? (
                      <span className="absolute inset-0 rounded-full bg-secondary" />
                    ) : (
                      <motion.span
                        layoutId="nav-pill"
                        className="absolute inset-0 rounded-full bg-secondary"
                        transition={{ type: "spring", stiffness: 400, damping: 34 }}
                      />
                    ))}
                  <span className="relative">{item.label}</span>
                </Link>
              );
            })}
          </nav>
        </div>

        <div className="flex items-center gap-2">
          <NetworkToggle />
          <SettingsMenu />
          {!ready ? (
            <Spinner />
          ) : authenticated ? (
            <WalletMenu />
          ) : (
            <button
              onClick={login}
              className="rounded-full bg-primary px-6 py-2.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
            >
              Connect
            </button>
          )}
        </div>
      </div>
    </header>
  );
}

/**
 * Which chain everything talks to. Testnet is amber with a dot and mainnet is
 * not: at a glance, from across a room, nobody has to wonder whether the money
 * on screen is real.
 */
function NetworkToggle() {
  const { chainId, setChainId, chains } = useChain();
  return (
    <div
      role="group"
      aria-label="Network"
      className="flex items-center gap-0.5 rounded-full bg-secondary/50 p-0.5"
    >
      {chains.map((c) => {
        const on = c.id === chainId;
        return (
          <button
            key={c.id}
            type="button"
            onClick={() => setChainId(c.id)}
            aria-pressed={on}
            title={c.name}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full px-3 py-1.5 text-xs font-medium transition-colors",
              on
                ? c.testnet
                  ? "bg-warning/15 text-warning"
                  : "bg-background text-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {c.testnet && (
              <span
                className={cn(
                  "size-1.5 rounded-full bg-warning",
                  on ? "" : "opacity-50",
                )}
              />
            )}
            {c.short}
          </button>
        );
      })}
    </div>
  );
}

/** The connected wallet: address, balances, export, disconnect. */
function WalletMenu() {
  const { wallet, logout, exportWallet, authenticated } = useSession();
  const chain = useChain();
  const { open, setOpen, ref } = useDismissable();
  const [copied, setCopied] = useState(false);
  const [exportErr, setExportErr] = useState<string | null>(null);

  const address = wallet?.address;

  async function copy() {
    if (!address) return;
    try {
      await navigator.clipboard.writeText(address);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard is blocked outside a secure context; the address is on
      // screen in full in the title attribute either way.
    }
  }

  return (
    <div className="relative" ref={ref}>
      <button
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        aria-label="Wallet"
        className="rounded-full bg-primary px-6 py-2.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
      >
        {address ? shortHash(address) : "Connected"}
      </button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-2 w-80 space-y-3 rounded-xl border border-border bg-popover p-4 text-sm shadow-xl">
          {!address ? (
            <p className="text-muted-foreground">
              No embedded wallet yet — create one from the settings menu.
            </p>
          ) : (
            <>
              <div className="flex items-center justify-between gap-3">
                <a
                  href={basescanAddress(address)}
                  target="_blank"
                  rel="noreferrer"
                  title={address}
                  className="tnum underline-offset-4 hover:underline"
                >
                  {shortHash(address)}
                </a>
                <button
                  onClick={() => void copy()}
                  aria-label="Copy wallet address"
                  className="rounded-md p-1.5 text-muted-foreground transition-colors hover:text-foreground"
                >
                  {copied ? (
                    <Check className="size-4 text-positive" />
                  ) : (
                    <Copy className="size-4" />
                  )}
                </button>
              </div>

              <p className="text-xs text-muted-foreground">
                Balances on {chain.name}
              </p>
              <Balances address={address} enabled={open && authenticated} />

              {exportErr && (
                <p className="text-xs break-words text-destructive">{exportErr}</p>
              )}
              <div className="flex gap-2 pt-1">
                <MenuButton
                  onClick={() => {
                    setExportErr(null);
                    exportWallet().catch((e: unknown) =>
                      setExportErr(e instanceof Error ? e.message : String(e)),
                    );
                  }}
                >
                  Export
                </MenuButton>
                <MenuButton onClick={() => void logout()}>Disconnect</MenuButton>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * USDC and ETH on the active chain.
 *
 * GET /v1/portfolio already carries onchain ERC-20 balances, so that is the
 * first source — but it is empty when TOKEN_API_JWT is unset, and it never
 * carries native ETH. Both gaps fall back to a direct RPC read. If that fails
 * too the figure is a dash and a reason: a fabricated zero looks like an empty
 * wallet, which is a different and much worse claim than "we do not know".
 */
function Balances({ address, enabled }: { address: string; enabled: boolean }) {
  const chain = useChain();
  const portfolio = useApi<Portfolio>(enabled ? "/v1/portfolio" : null);
  const read = useAsync<WalletBalances | null>(
    `balances:${address}:${chain.chainId}:${enabled}`,
    () =>
      enabled
        ? readBalances(address, chain.chainId).catch(() => ({
            eth: null,
            usdc: null,
          }))
        : Promise.resolve(null),
  );
  const direct = read.data;

  const indexed = portfolio.data?.onchain_available
    ? (portfolio.data.onchain?.balances ?? [])
    : null;
  const fromIndex = indexed?.find((b) => b.symbol === "USDC")?.value ?? null;

  const usdc = fromIndex ?? direct?.usdc ?? null;
  const eth = direct?.eth ?? null;
  const loading = read.loading || portfolio.loading;

  return (
    <div className="space-y-2">
      <BalanceRow symbol="USDC" amount={usdc} loading={loading} />
      <BalanceRow symbol="ETH" amount={eth} loading={loading} />
      {!loading && (usdc === null || eth === null) && (
        <p className="text-xs text-warning">
          A dash means the balance could not be read from {chain.name} — not
          that it is zero.
        </p>
      )}
    </div>
  );
}

function BalanceRow({
  symbol,
  amount,
  loading,
}: {
  symbol: string;
  amount: number | null;
  loading: boolean;
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-muted-foreground">{symbol}</span>
      <span className={cn("tnum", amount === null && !loading && "text-warning")}>
        {loading ? "…" : amount === null ? "—" : fmtToken(amount)}
      </span>
    </div>
  );
}

/** Delegation, wallet creation and logout live here so every control stays reachable. */
function SettingsMenu() {
  const {
    ready,
    authenticated,
    logout,
    wallet,
    createWallet,
    creatingWallet,
    me,
    meError,
    delegate,
    revoke,
    delegating,
    delegationError,
    signerConfigured,
  } = useSession();
  const { open, setOpen, ref } = useDismissable();

  return (
    <div className="relative" ref={ref}>
      <button
        onClick={() => setOpen((o) => !o)}
        aria-label="Settings"
        aria-expanded={open}
        className="rounded-full p-2.5 text-muted-foreground transition-colors hover:text-foreground"
      >
        <Settings className="size-5" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-2 w-72 space-y-3 rounded-xl border border-border bg-popover p-4 text-sm shadow-xl">
          {!ready ? (
            <Spinner label="Loading…" />
          ) : !authenticated ? (
            <p className="text-muted-foreground">Connect to manage your wallet.</p>
          ) : (
            <>
              <Line
                label="Wallet"
                value={wallet ? shortHash(wallet.address) : "none yet"}
              />
              <Line
                label="Delegation"
                value={me?.delegated ? "granted" : "not granted"}
                tone={me?.delegated ? "good" : "warn"}
              />
              {!signerConfigured && (
                <p className="text-xs text-warning">
                  NEXT_PUBLIC_PRIVY_SIGNER_ID is unset — delegation cannot be granted.
                </p>
              )}
              {meError && <p className="text-xs text-destructive">{meError}</p>}
              {delegationError && (
                <p className="text-xs text-destructive break-words">
                  {delegationError}
                </p>
              )}
              <div className="flex flex-wrap gap-2 pt-1">
                {!wallet && (
                  <MenuButton
                    onClick={() => void createWallet()}
                    disabled={creatingWallet}
                  >
                    {creatingWallet ? "Creating…" : "Create wallet"}
                  </MenuButton>
                )}
                {wallet && !me?.delegated && (
                  <MenuButton
                    onClick={() => void delegate()}
                    disabled={delegating || !signerConfigured}
                  >
                    {delegating ? "Working…" : "Enable delegation"}
                  </MenuButton>
                )}
                {wallet && me?.delegated && (
                  <MenuButton onClick={() => void revoke()} disabled={delegating}>
                    {delegating ? "Working…" : "Revoke delegation"}
                  </MenuButton>
                )}
                <MenuButton onClick={() => void logout()}>Log out</MenuButton>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

function MenuButton({
  children,
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <button
      {...rest}
      className="rounded-lg border border-border bg-secondary/50 px-3 py-1.5 text-xs transition-colors hover:bg-secondary disabled:cursor-not-allowed disabled:text-muted-foreground"
    >
      {children}
    </button>
  );
}

function Line({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: "good" | "warn";
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-muted-foreground">{label}</span>
      <span
        className={cn(
          "tnum",
          tone === "good"
            ? "text-positive"
            : tone === "warn"
              ? "text-warning"
              : "text-foreground",
        )}
      >
        {value}
      </span>
    </div>
  );
}
