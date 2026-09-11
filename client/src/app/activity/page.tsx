"use client";

import Link from "next/link";
import { fmtTime, fmtUsd } from "@/lib/api";
import { useApi } from "@/lib/session";
import {
  Empty,
  ErrorBox,
  Panel,
  RequireAuth,
  Spinner,
  StatusPill,
  Steps,
  TxLink,
} from "@/components/ui";
import type { Execution } from "@/lib/types";

export default function ActivityPage() {
  return (
    <RequireAuth>
      <Activity />
    </RequireAuth>
  );
}

function Activity() {
  const { data, error, loading, reload } =
    useApi<Execution[] | null>("/v1/executions?limit=100");

  if (loading) return <Spinner label="Loading executions…" />;
  if (error) return <ErrorBox message={error} onRetry={reload} />;

  const rows = data ?? [];

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Activity</h1>
        <p className="mt-1 text-sm text-zinc-500">
          Every execution is recorded before it is submitted, so nothing is
          invisible — including transactions whose receipt never came back.
        </p>
      </div>

      {rows.length === 0 ? (
        <Empty>No executions yet.</Empty>
      ) : (
        <Panel>
          <ul className="divide-y divide-zinc-800">
            {rows.map((e) => (
              <li key={e.id} className="py-3 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <span className="font-mono text-sm">{e.asset}</span>
                  <span className="text-xs uppercase tracking-wider text-zinc-500">
                    {e.kind}
                  </span>
                  <StatusPill status={e.status} />
                  <span className="font-mono text-sm text-zinc-300">
                    {fmtUsd(e.amount_usd)}
                  </span>
                  {e.tx_hash && <TxLink hash={e.tx_hash} />}
                  <span className="ml-auto text-xs text-zinc-600">
                    {fmtTime(e.created_at)}
                  </span>
                </div>

                {(e.from_venue || e.to_venue) && (
                  <div className="mt-1 break-all font-mono text-[11px] text-zinc-600">
                    {e.from_venue ? `${e.from_venue} → ` : ""}
                    {e.to_venue ?? "—"}
                  </div>
                )}

                {e.status === "pending" && (
                  <p className="mt-1 text-xs text-amber-400">
                    The receipt poll timed out. This is not a failure — the
                    transaction may still be in the mempool. Check the hash on
                    Basescan before resubmitting.
                  </p>
                )}

                {e.error && (
                  <p className="mt-1 text-xs text-red-400">{e.error}</p>
                )}

                {e.steps && e.steps.length > 0 && <Steps steps={e.steps} />}

                {e.basket_id && (
                  <Link
                    href={`/baskets/${e.basket_id}`}
                    className="mt-1 inline-block text-[11px] text-zinc-500 hover:text-zinc-300"
                  >
                    view basket
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}
