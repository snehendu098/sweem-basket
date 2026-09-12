"use client";

import { useCallback, useState } from "react";
import { toast } from "sonner";
import { ApiError, basescanTx, fmtUsd, shortHash } from "@/lib/api";
import { Button, SettleReport } from "@/components/ui";
import type { SettleResult } from "@/lib/types";

const settledLeg = (status: string) =>
  status === "submitted" || status === "confirmed";

type Detail = { key: string; status: number; data: SettleResult };

export type NotifyOpts = {
  key: string;
  verb: string;
  successTitle?: string;
};

export function useSettleToast() {
  const [detail, setDetail] = useState<Detail | null>(null);
  const clear = useCallback(() => setDetail(null), []);

  const notify = useCallback(
    (status: number, data: SettleResult, o: NotifyOpts): boolean => {
      const legs = data.legs ?? [];
      const hash = legs.find((l) => l.tx_hash)?.tx_hash;
      const show = () => setDetail({ key: o.key, status, data });
      const details = legs.length > 0 ? { label: "Details", onClick: show } : undefined;

      if (status === 207 || data.failed_legs > 0) {
        const done = legs.filter((l) => settledLeg(l.status)).length;
        toast.warning(
          legs.length > 0
            ? `${done} of ${legs.length} legs settled`
            : `Partial — ${data.failed_legs} failed`,
          {
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
          `${o.verb}${data.submitted_usd !== undefined ? ` ${fmtUsd(data.submitted_usd)}` : ""}`,
        {
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

export function toastError(e: unknown) {
  const message = e instanceof Error ? e.message : String(e);
  const fn = e instanceof ApiError && e.status === 412 ? toast.warning : toast.error;
  fn(message, { duration: Infinity });
}
