"use client";

import { useCallback, useState } from "react";
import { toast } from "sonner";
import { ApiError, basescanTx, fmtUsd, shortHash } from "@/lib/api";
import { Button, SettleReport } from "@/components/ui";
import type { SettleResult } from "@/lib/types";

/** The API's own vocabulary for "this leg went out". */
const settledLeg = (status: string) =>
  status === "submitted" || status === "confirmed";

type Detail = { key: string; status: number; data: SettleResult };

export type NotifyOpts = {
  /** Ties the drill-down to whatever produced it — usually the basket id. */
  key: string;
  /** Past tense, e.g. "Deposited". Titles the clean case. */
  verb: string;
  /** Replaces the clean-case title when nothing needed to move. */
  successTitle?: string;
};

/**
 * A settle result is a notification, not a page section. The common case — one
 * leg, submitted, done — is a line that disappears; the partial and pending
 * cases are the ones worth reading, so they stay until dismissed and keep the
 * full per-leg breakdown one click away.
 *
 * Returns `notify`, which reports the outcome and answers whether it was clean
 * (so a form can reset itself), plus the `detail` the caller renders when the
 * user asks to see the legs.
 */
export function useSettleToast() {
  const [detail, setDetail] = useState<Detail | null>(null);
  const clear = useCallback(() => setDetail(null), []);

  const notify = useCallback(
    (status: number, data: SettleResult, o: NotifyOpts): boolean => {
      const legs = data.legs ?? [];
      const hash = legs.find((l) => l.tx_hash)?.tx_hash;
      const show = () => setDetail({ key: o.key, status, data });
      const details = legs.length > 0 ? { label: "Details", onClick: show } : undefined;

      // 207 is a partial result, never an error: warning tone, and the legs
      // that did move are named in the count rather than lost in a blanket
      // failure.
      if (status === 207 || data.failed_legs > 0) {
        const done = legs.filter((l) => settledLeg(l.status)).length;
        toast.warning(
          legs.length > 0
            ? `${done} of ${legs.length} legs settled`
            : `Partial — ${data.failed_legs} failed`,
          {
            // The executor's own words. Paraphrasing them loses the only thing
            // that makes this debuggable.
            description:
              legs.find((l) => l.status === "failed")?.reason ??
              (data.pending_legs > 0
                ? `${data.pending_legs} still pending — the receipt poll timed out.`
                : undefined),
            duration: Infinity,
            action: details,
          },
        );
        return false;
      }

      // Pending is amber, never red: the transaction may still land, so the
      // one thing that must survive is "do not just press it again".
      if (data.pending_legs > 0) {
        toast.warning(
          `${o.verb}, ${data.pending_legs} leg${data.pending_legs === 1 ? "" : "s"} pending`,
          {
            description:
              "The receipt poll timed out; it may still land. Check the hash before retrying — a retry can send the same money twice.",
            duration: Infinity,
            action: details,
          },
        );
        return false;
      }

      toast.success(
        o.successTitle ??
          // submitted_usd absent means the API did not state an amount. No
          // amount is then shown; none is invented.
          `${o.verb}${data.submitted_usd !== undefined ? ` ${fmtUsd(data.submitted_usd)}` : ""}`,
        {
          // Truncated for reading, whole hash in the link.
          description: hash ? `tx ${shortHash(hash)}` : undefined,
          duration: 6000,
          action: hash
            ? {
                label: "Basescan",
                onClick: () =>
                  window.open(basescanTx(hash), "_blank", "noopener,noreferrer"),
              }
            : details,
        },
      );
      return true;
    },
    [],
  );

  return { notify, detail, clear };
}

/** The 207 drill-down: the old report, now opened on request. */
export function SettleDetail({
  detail,
  onClose,
}: {
  detail: { status: number; data: SettleResult };
  onClose: () => void;
}) {
  return (
    <div className="space-y-2">
      <SettleReport result={detail.data} status={detail.status} />
      <Button variant="ghost" className="w-full" onClick={onClose}>
        Hide breakdown
      </Button>
    </div>
  );
}

/**
 * Errors, verbatim. A 412 is a missing precondition — delegation, usually —
 * and reads as the next thing to do, so it is amber and not red. Everything
 * stays until dismissed: an error about money should not vanish on a timer.
 */
export function toastError(e: unknown) {
  const message = e instanceof Error ? e.message : String(e);
  const fn = e instanceof ApiError && e.status === 412 ? toast.warning : toast.error;
  fn(message, { duration: Infinity });
}
