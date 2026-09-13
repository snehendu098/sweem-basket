import { ChevronDown } from "lucide-react";
import { QUOTE_ASSET } from "@/lib/api";

const QA: readonly { q: string; a: string }[] = [
  {
    q: "Where do my funds actually sit?",
    a: "In your own wallet. There is no pooled vault and no share token: each deposit is supplied to a lending venue from your address, and the position is yours. Nothing here can hold your money.",
  },
  {
    q: "Then how does it move anything?",
    a: "You grant a delegated signer in Privy. It is bound to a policy that only permits deposits and withdrawals at allowlisted venues and swaps through one allowlisted router, on the chains you use. It cannot transfer to another address, send native value, or sign arbitrary messages. Revoke it in Privy at any time and the basket stops moving.",
  },
  {
    q: "How is a venue picked?",
    a: "Highest net rate among venues that clear three gates: published by the indexer, above the liquidity floor, and on the executor's allowlist. A venue paying more that fails any gate is skipped, and the reason is shown rather than hidden.",
  },
  {
    q: "What does “BTC → cbBTC” mean?",
    a: "You pick the exposure, not the instrument. The instrument is resolved at the moment you deposit, so a shared basket routes to whatever is best for you then rather than what was best when it was created. It is fixed for that deposit and not re-picked afterwards.",
  },
  {
    q: "What if a token has no yield venue?",
    a: `It is bought through Uniswap and held in your wallet, earning nothing. That is stated on the leg rather than quietly dropped, and withdrawing swaps it back to ${QUOTE_ASSET}.`,
  },
  {
    q: "Does it rebalance?",
    a: "A keeper moves a leg when a better allowlisted venue beats its entry rate by more than the threshold — enough to cover the round trip. Held tokens are never moved, since they hold the asset rather than the funding asset.",
  },
  {
    q: "What does it cost?",
    a: `No protocol fee. You pay gas and whatever the swap costs; each swap is quoted first and sent with a minimum output, so a route that moved against you reverts instead of filling badly.`,
  },
];

export function Faq() {
  return (
    <section className="space-y-2">
      <h2 className="px-1 pb-1 text-sm font-medium text-muted-foreground">
        Questions
      </h2>
      {QA.map(({ q, a }) => (
        <details
          key={q}
          className="group rounded-xl border border-border bg-card/60 px-4 [&[open]]:bg-card"
        >
          <summary className="flex cursor-pointer list-none items-center gap-3 py-3.5 text-sm font-medium marker:hidden">
            {q}
            <ChevronDown className="ml-auto size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
          </summary>
          <p className="pb-4 pr-7 text-sm leading-relaxed text-muted-foreground">
            {a}
          </p>
        </details>
      ))}
    </section>
  );
}
