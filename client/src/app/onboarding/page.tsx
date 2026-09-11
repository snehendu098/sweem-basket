"use client";

import Link from "next/link";
import { basescanAddress } from "@/lib/api";
import { useSession } from "@/lib/session";
import { Button, ErrorBox, Panel, Spinner } from "@/components/ui";

function Step({
  n,
  title,
  done,
  children,
}: {
  n: number;
  title: string;
  done: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex gap-4">
      <div
        className={`mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-xs font-mono ${
          done
            ? "border-emerald-700 bg-emerald-950/60 text-emerald-400"
            : "border-zinc-700 text-zinc-500"
        }`}
      >
        {done ? "✓" : n}
      </div>
      <div className="min-w-0 flex-1 pb-6">
        <h3 className="text-sm font-semibold text-zinc-100">{title}</h3>
        <div className="mt-2 text-sm text-zinc-400">{children}</div>
      </div>
    </div>
  );
}

export default function Onboarding() {
  const {
    ready,
    authenticated,
    login,
    wallet,
    createWallet,
    creatingWallet,
    me,
    meError,
    syncing,
    refreshMe,
    delegate,
    revoke,
    delegating,
    delegationError,
    signerConfigured,
  } = useSession();

  if (!ready) return <Spinner label="Starting Privy…" />;

  const delegated = wallet?.delegated ?? false;

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">
          Set up your wallet
        </h1>
        <p className="mt-2 max-w-2xl text-sm text-zinc-400">
          Three things have to be true before the protocol can act for you. None
          of them move your money, and the last one is reversible.
        </p>

        <div className="mt-8">
          <Step n={1} title="Log in" done={authenticated}>
            {authenticated ? (
              <span className="text-zinc-500">Signed in with Privy.</span>
            ) : (
              <Button onClick={login}>Log in with email or wallet</Button>
            )}
          </Step>

          <Step n={2} title="Embedded wallet" done={!!wallet}>
            {wallet ? (
              <div className="space-y-1">
                <a
                  href={basescanAddress(wallet.address)}
                  target="_blank"
                  rel="noreferrer"
                  className="break-all font-mono text-xs text-sky-400 hover:underline"
                >
                  {wallet.address}
                </a>
                <p className="text-xs text-zinc-500">
                  Created and secured by Privy. Only you and the signers you
                  approve can move funds from it.
                </p>
              </div>
            ) : authenticated ? (
              <>
                <p>
                  Privy normally creates this on login. If it did not, create it
                  now.
                </p>
                <Button
                  className="mt-2"
                  disabled={creatingWallet}
                  onClick={() => void createWallet()}
                >
                  {creatingWallet ? "Creating…" : "Create embedded wallet"}
                </Button>
              </>
            ) : (
              <span className="text-zinc-600">Waiting for login.</span>
            )}
          </Step>

          <Step n={3} title="Registered with the protocol" done={!!me}>
            {meError ? (
              <ErrorBox message={meError} onRetry={() => void refreshMe()} />
            ) : syncing ? (
              <Spinner label="Binding wallet to your account…" />
            ) : me ? (
              <p className="text-xs text-zinc-500">
                Your Privy DID is bound to this wallet address. The backend
                stores no keys — only the address and Privy&apos;s wallet ID.
              </p>
            ) : (
              <span className="text-zinc-600">Waiting for a wallet.</span>
            )}
          </Step>

          <Step n={4} title="Delegate execution" done={delegated}>
            <p>
              Add the protocol&apos;s signer to your wallet so it can route your
              funds between lending venues while you are offline. It cannot
              transfer funds out of your wallet, and you can remove it at any
              time.
            </p>
            {!signerConfigured && (
              <div className="mt-3 rounded border border-amber-900 bg-amber-950/30 p-3 text-xs text-amber-300">
                <code>NEXT_PUBLIC_PRIVY_SIGNER_ID</code> is not set, so there is
                no signer to add. Register the executor&apos;s authorization
                public key as a key quorum in the Privy dashboard and put its ID
                in <code>.env.local</code>.
              </div>
            )}
            {delegationError && (
              <div className="mt-3">
                <ErrorBox message={delegationError} />
              </div>
            )}
            <div className="mt-3 flex flex-wrap items-center gap-3">
              {delegated ? (
                <>
                  <Button
                    variant="danger"
                    disabled={delegating}
                    onClick={() => void revoke()}
                  >
                    {delegating ? "Revoking…" : "Revoke delegation"}
                  </Button>
                  <Link
                    href="/explore"
                    className="text-sm text-emerald-400 hover:underline"
                  >
                    Pick a basket →
                  </Link>
                </>
              ) : (
                <Button
                  disabled={!wallet || !me || delegating || !signerConfigured}
                  onClick={() => void delegate()}
                >
                  {delegating ? "Waiting for Privy…" : "Grant delegation"}
                </Button>
              )}
            </div>
          </Step>
        </div>
      </div>

      <aside className="space-y-4">
        <Panel title="Security model">
          <ul className="space-y-3 text-sm text-zinc-400">
            <li>
              <span className="text-zinc-200">Custody:</span> none. The protocol
              never holds your assets; they sit in your embedded wallet the
              whole time.
            </li>
            <li>
              <span className="text-zinc-200">Scope:</span> the signer is bound
              by a Privy policy to an allowlist of lending venues.
            </li>
            <li>
              <span className="text-zinc-200">Keys:</span> signing happens in
              Privy&apos;s enclave. No signer, including this app, ever sees your
              private key.
            </li>
            <li>
              <span className="text-zinc-200">Exit:</span> revoke above. Removing
              the signer takes effect immediately and moves nothing.
            </li>
          </ul>
        </Panel>

        {me && (
          <Panel title="Backend record">
            <dl className="space-y-2 text-xs">
              <Row label="Privy DID" value={me.privy_did} />
              <Row label="Wallet" value={me.wallet_address} />
              <Row
                label="Privy wallet ID"
                value={
                  me.privy_wallet_id ||
                  "not set — Privy exposes it once the wallet has a signer"
                }
              />
              <Row label="Delegated" value={String(me.delegated)} />
            </dl>
          </Panel>
        )}
      </aside>
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <dt className="text-[11px] uppercase tracking-wider text-zinc-500">
        {label}
      </dt>
      <dd className="break-all font-mono text-zinc-300">{value}</dd>
    </div>
  );
}
