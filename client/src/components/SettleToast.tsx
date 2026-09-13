"use client";

import { useCallback, useState } from "react";
import {
  CircleCheckIcon,
  Loader2Icon,
  OctagonXIcon,
  TriangleAlertIcon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";
import {
  ApiError,
  QUOTE_ASSET,
  basescanTx,
  displayAsset,
  fmtUsd,
  shortHash,
} from "@/lib/api";
import { Button, SettleReport } from "@/components/ui";
import { ProtocolStack } from "@/components/ProtocolIcon";
import { cn } from "@/lib/utils";
import type { SettleResult } from "@/lib/types";

const settledLeg = (status: string) =>
  status === "submitted" || status === "confirmed";

// Uniswap first when any leg swapped, then each venue the money touched, in
// the order the legs ran. Deduped: two cbBTC legs at Moonwell are one mark.
function protocolsTouched(legs: SettleResult["legs"]): string[] {
  const settled = (legs ?? []).filter((l) => settledLeg(l.status));
  const out = settled.some((l) => l.asset !== QUOTE_ASSET) ? ["uniswap"] : [];
  for (const l of settled) {
    if (l.project && !out.includes(l.project)) out.push(l.project);
  }
  return out;
}

type Tone = "success" | "warning" | "error" | "loading";

const TONE_ICON = {
  success: <CircleCheckIcon className="size-4 text-positive" />,
  warning: <TriangleAlertIcon className="size-4 text-warning" />,
  error: <OctagonXIcon className="size-4 text-destructive" />,
  loading: <Loader2Icon className="size-4 animate-spin text-muted-foreground" />,
} satisfies Record<Tone, React.ReactNode>;

type ToastAction = { label: string; onClick: () => void };

function ToastCard({
  tone,
  projects = [],
  title,
  description,
  actions = [],
  onDismiss,
}: {
  tone: Tone;
  projects?: readonly string[];
  title: string;
  description?: string;
  actions?: ToastAction[];
  onDismiss?: () => void;
}) {
  return (
    <div className="w-[356px] max-w-[calc(100vw-2rem)] overflow-hidden rounded-2xl border border-border bg-popover p-4 text-popover-foreground shadow-lg">
      <div className="flex items-start gap-2.5">
        <span className="mt-0.5 shrink-0">{TONE_ICON[tone]}</span>
        <ProtocolStack projects={projects} size={22} />
        <p
          className={cn(
            "min-w-0 flex-1 text-sm leading-5 font-medium break-words",
            tone === "warning" && "text-warning",
            tone === "error" && "text-destructive",
          )}
        >
          {title}
        </p>
        {onDismiss && (
          <button
            type="button"
            aria-label="Dismiss"
            onClick={onDismiss}
            className="-mr-1 -mt-1 shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          >
            <XIcon className="size-3.5" />
          </button>
        )}
      </div>
      {description && (
        <p className="mt-1.5 text-xs leading-4 break-words text-muted-foreground">
          {description}
        </p>
      )}
      {actions.length > 0 && (
        <div className="mt-3 flex flex-wrap justify-end gap-2">
          {actions.map((a) => (
            <Button
              key={a.label}
              variant="ghost"
              className="px-3 py-1 text-xs"
              onClick={a.onClick}
            >
              {a.label}
            </Button>
          ))}
        </div>
      )}
    </div>
  );
}

type CardProps = React.ComponentProps<typeof ToastCard>;

const showCard = (
  props: Omit<CardProps, "actions"> & {
    actions?: (dismiss: () => void) => ToastAction[];
  },
  duration: number,
) =>
  toast.custom(
    (id) => {
      const dismiss = () => toast.dismiss(id);
      const actions = props.actions?.(dismiss) ?? [];
      if (actions.length === 0 && duration === Infinity)
        actions.push({ label: "Dismiss", onClick: dismiss });
      return <ToastCard {...props} actions={actions} onDismiss={dismiss} />;
    },
    { duration },
  );

export function toastLoading(title: string, description?: string) {
  return showCard({ tone: "loading", title, description }, Infinity);
}

export const dismissToast = (id: string | number) => toast.dismiss(id);

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
      const projects = protocolsTouched(legs);
      const detailsAction = (dismiss: () => void): ToastAction[] =>
        legs.length > 0
          ? [
              {
                label: "Details",
                onClick: () => {
                  setDetail({ key: o.key, status, data });
                  dismiss();
                },
              },
            ]
          : [];
      const basescanAction = (): ToastAction[] =>
        hash
          ? [
              {
                label: "Basescan",
                onClick: () =>
                  window.open(basescanTx(hash), "_blank", "noopener,noreferrer"),
              },
            ]
          : [];

      if (status === 207 || data.failed_legs > 0) {
        const done = legs.filter((l) => settledLeg(l.status)).length;
        showCard(
          {
            tone: "warning",
            projects,
            title:
              legs.length > 0
                ? `${done} of ${legs.length} legs settled`
                : `Partial — ${data.failed_legs} failed`,
            description:
              legs.find((l) => l.status === "failed")?.reason ??
              (data.pending_legs > 0
                ? `${data.pending_legs} still pending — the receipt poll timed out.`
                : undefined),
            actions: detailsAction,
          },
          Infinity,
        );
        return false;
      }

      if (data.pending_legs > 0) {
        showCard(
          {
            tone: "warning",
            projects,
            title: `${o.verb}, ${data.pending_legs} leg${data.pending_legs === 1 ? "" : "s"} pending`,
            description:
              "The receipt poll timed out; it may still land. Check the hash before retrying — a retry can send the same money twice.",
            actions: detailsAction,
          },
          Infinity,
        );
        return false;
      }

      const swapped = legs.filter(
        (l) => settledLeg(l.status) && l.asset !== QUOTE_ASSET,
      );
      showCard(
        {
          tone: "success",
          projects,
          title:
            o.successTitle ??
            `${o.verb}${data.submitted_usd !== undefined ? ` ${fmtUsd(data.submitted_usd)}` : ""}`,
          description:
            [
              swapped.length > 0
                ? `swapped on Uniswap: ${swapped.map((l) => displayAsset(l.asset)).join(", ")}`
                : null,
              hash ? `tx ${shortHash(hash)}` : null,
            ]
              .filter(Boolean)
              .join(" · ") || undefined,
          actions: (dismiss) =>
            hash ? basescanAction() : detailsAction(dismiss),
        },
        6000,
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
  const soft = e instanceof ApiError && e.status === 412;
  showCard({ tone: soft ? "warning" : "error", title: message }, Infinity);
}
