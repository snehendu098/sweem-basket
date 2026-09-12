"use client";

import { useMemo } from "react";
import {
  Background,
  Handle,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { CircleDashed, Wallet } from "lucide-react";
import { displayAsset, displayProject, fmtPct, fmtUsd } from "@/lib/api";
import { TokenIcon } from "@/components/TokenIcon";

export type FlowLeg = {
  asset: string;
  amountUsd: number | null;
  venue: { project: string; apy: number } | null;
  idle: boolean;
  reason?: string;
};

const PROTOCOL_MARK: Record<string, string> = {
  "aave-v3": "aave-v3.png",
  "compound-v3": "compound-v3.png",
  "morpho-blue": "morpho-blue.svg",
  moonwell: "moonwell.png",
};

type CardData = {
  [k: string]: unknown;
  title: string;
  value?: string;
  note?: string;
  symbol?: string;
  project?: string;
  wallet?: boolean;
  tone?: "idle" | "muted";
  valueTone?: "positive";
  ends?: "left" | "right";
};

type CardNode = Node<CardData, "card">;

const BASE = "flex h-full items-center gap-2.5 rounded-xl border px-3 py-2.5";

const TONE: Record<string, string> = {
  idle: "border-warning/30 bg-card text-warning",
  muted: "border-dashed border-border bg-card text-muted-foreground",
};

function Mark({ data }: { data: CardData }) {
  if (data.wallet)
    return (
      <span className="grid size-[22px] shrink-0 place-items-center rounded-full bg-foreground/10">
        <Wallet className="size-3.5" />
      </span>
    );
  if (data.symbol) return <TokenIcon symbol={data.symbol} size={22} />;
  if (data.tone === "idle")
    return <CircleDashed className="size-[22px] shrink-0" />;
  const file = data.project ? PROTOCOL_MARK[data.project] : undefined;
  if (!file) return null;
  return (
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={`/protocols/${file}`}
      alt=""
      width={22}
      height={22}
      className="size-[22px] shrink-0 rounded-full"
    />
  );
}

function Card({ data }: NodeProps<CardNode>) {
  const second = data.value ?? data.note;
  return (
    <div className={`${BASE} ${TONE[data.tone as string] ?? "border-border bg-card"}`}>
      {data.ends !== "left" && (
        <Handle type="target" position={Position.Left} className="!opacity-0" />
      )}
      <Mark data={data} />
      <div className="min-w-0">
        <div className="truncate text-sm font-medium leading-tight">
          {data.title}
        </div>
        {second && (
          <div
            className={`mt-1 truncate text-[11px] leading-tight ${
              data.value ? "tnum" : ""
            } ${
              data.valueTone === "positive"
                ? "text-positive"
                : "text-muted-foreground"
            }`}
          >
            {second}
          </div>
        )}
      </div>
      {data.ends !== "right" && (
        <Handle type="source" position={Position.Right} className="!opacity-0" />
      )}
    </div>
  );
}

const nodeTypes = { card: Card };

// Uniform height: unequal heights bow same-row edges.
const ROW = 88;
const HEIGHT = 64;
const WIDTH = { basket: 150, asset: 160, venue: 180 };
const COL = { basket: 0, asset: 200, venue: 410 };

const defaultEdgeOptions = { type: "straight" } as const;

export function BasketFlow({
  legs,
  emptyLabel,
}: {
  legs: FlowLeg[];
  emptyLabel: string;
}) {
  const { nodes, edges } = useMemo(() => {
    const nodes: CardNode[] = [];
    const edges: Edge[] = [];
    if (legs.length === 0) return { nodes, edges };

    const mid = ((legs.length - 1) * ROW) / 2;
    nodes.push({
      id: "basket",
      type: "card",
      position: { x: COL.basket, y: mid },
      data: { title: "Your wallet", wallet: true, ends: "left" },
      style: { width: WIDTH.basket, height: HEIGHT },
      draggable: false,
    });

    legs.forEach((l, i) => {
      const y = i * ROW;
      const asset = `asset:${l.asset}`;
      nodes.push({
        id: asset,
        type: "card",
        position: { x: COL.asset, y },
        data: {
          title: displayAsset(l.asset),
          ...(l.amountUsd === null
            ? { note: "value unknown" }
            : { value: fmtUsd(l.amountUsd) }),
          symbol: l.asset,
        },
        style: { width: WIDTH.asset, height: HEIGHT },
        draggable: false,
      });
      edges.push({ id: `e-basket-${asset}`, source: "basket", target: asset });

      if (l.idle) {
        const idle = `idle:${l.asset}`;
        nodes.push({
          id: idle,
          type: "card",
          position: { x: COL.venue, y },
          data: {
            title: "Unplaced",
            note: "in wallet, earning nothing",
            tone: "idle",
            ends: "right",
          },
          style: { width: WIDTH.venue, height: HEIGHT },
          draggable: false,
        });
        edges.push({ id: `e-${asset}-${idle}`, source: asset, target: idle });
        return;
      }

      if (!l.venue) {
        nodes.push({
          id: `none:${l.asset}`,
          type: "card",
          position: { x: COL.venue, y },
          data: {
            title: "Not routed",
            note: l.reason || "no venue",
            tone: "muted",
            ends: "right",
          },
          style: { width: WIDTH.venue, height: HEIGHT },
          draggable: false,
        });
        return;
      }

      const venue = `venue:${l.asset}`;
      nodes.push({
        id: venue,
        type: "card",
        position: { x: COL.venue, y },
        data: {
          title: displayProject(l.venue.project),
          value: fmtPct(l.venue.apy),
          valueTone: "positive",
          project: l.venue.project,
          ends: "right",
        },
        style: { width: WIDTH.venue, height: HEIGHT },
        draggable: false,
      });
      edges.push({ id: `e-${asset}-${venue}`, source: asset, target: venue });
    });

    return { nodes, edges };
  }, [legs]);

  if (legs.length === 0) {
    return (
      <div className="flex h-44 items-center justify-center px-6 text-center text-sm text-muted-foreground">
        {emptyLabel}
      </div>
    );
  }

  return (
    <div
      className="pb-3"
      style={{ height: Math.max(200, legs.length * ROW + 76) }}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        defaultEdgeOptions={defaultEdgeOptions}
        fitView
        fitViewOptions={{ padding: 0.14 }}
        proOptions={{ hideAttribution: true }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        panOnDrag={false}
        panOnScroll={false}
        zoomOnScroll={false}
        zoomOnPinch={false}
        zoomOnDoubleClick={false}
        preventScrolling={false}
      >
        <Background gap={20} size={1} className="opacity-40" />
      </ReactFlow>
    </div>
  );
}
