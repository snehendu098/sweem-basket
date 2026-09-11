"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { CHAIN, fmtCompactUsd, fmtPct, market } from "@/lib/api";
import { useAsync, useSession } from "@/lib/session";
import { Button, Empty, ErrorBox, Panel, Spinner } from "@/components/ui";

const CLAIMS = [
  {
    title: "Your wallet holds the money",
    body: "There is no pooled vault and no share accounting. A basket is a published weight config; subscribing makes your own Privy embedded wallet follow those weights.",
  },
  {
    title: "A bounded signer, not a custodian",
    body: "You add the protocol's key quorum as a signer on your wallet, scoped by a policy to an allowlist of lending venues. It can route between those venues and nothing else.",
  },
  {
    title: "Revocable in one click",
    body: "Remove the signer whenever you want. Your funds do not move, and the protocol immediately loses the ability to act.",
  },
];

export default function Landing() {
  const { ready, authenticated } = useSession();
  const router = useRouter();
  const { data, error, loading } = useAsync("assets", () => market.assets());

  useEffect(() => {
    if (ready && authenticated) router.replace("/explore");
  }, [ready, authenticated, router]);

  const assets = data?.assets ?? [];

  return (
    <div className="space-y-10">
      <section className="max-w-3xl">
        <h1 className="text-4xl font-semibold tracking-tight text-zinc-50">
          Yield baskets that never take your money.
        </h1>
        <p className="mt-4 text-lg text-zinc-400">
          Deposit USDC, pick a basket of assets. Each slice is routed to the
          best-yielding venue on {CHAIN} and rebalanced as rates move — executed
          from your own wallet by a delegated signer you can revoke at any time.
        </p>
        <div className="mt-6 flex items-center gap-3">
          <LoginButton />
          <Link
            href="/explore"
            className="text-sm text-zinc-400 underline-offset-4 hover:text-zinc-200 hover:underline"
          >
            Browse public baskets
          </Link>
        </div>
      </section>

      <section className="grid gap-4 md:grid-cols-3">
        {CLAIMS.map((c) => (
          <div
            key={c.title}
            className="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4"
          >
            <h3 className="text-sm font-semibold text-emerald-400">{c.title}</h3>
            <p className="mt-2 text-sm leading-relaxed text-zinc-400">
              {c.body}
            </p>
          </div>
        ))}
      </section>

      <Panel title={`Live venues on ${CHAIN}`}>
        {loading ? (
          <Spinner label="Reading market data…" />
        ) : error ? (
          <ErrorBox message={error} />
        ) : assets.length === 0 ? (
          <Empty>
            The market-data service reports no live venues right now.
          </Empty>
        ) : (
          <table className="w-full text-sm">
            <thead className="text-left text-[11px] uppercase tracking-wider text-zinc-500">
              <tr>
                <th className="pb-2 font-medium">Asset</th>
                <th className="pb-2 font-medium">Best APY</th>
                <th className="pb-2 font-medium">Venues</th>
                <th className="pb-2 font-medium">TVL</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-800">
              {assets.map((a) => (
                <tr key={`${a.chain}:${a.asset}`}>
                  <td className="py-2 font-mono">{a.asset}</td>
                  <td className="py-2 font-mono text-emerald-400">
                    {fmtPct(a.best_apy)}
                  </td>
                  <td className="py-2 font-mono text-zinc-400">{a.venues}</td>
                  <td className="py-2 font-mono text-zinc-400">
                    {fmtCompactUsd(a.total_tvl_usd)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
    </div>
  );
}

function LoginButton() {
  const { ready, login } = useSession();
  return (
    <Button disabled={!ready} onClick={login}>
      {ready ? "Log in with email or wallet" : "Loading…"}
    </Button>
  );
}
