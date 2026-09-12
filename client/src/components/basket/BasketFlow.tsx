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

/**
 * One row of the diagram: a slice of the basket, the asset it is held as, and
 * the venue it sits in. `venue` null means the money has nowhere to go — the
 * asset node is drawn with no venue edge and `reason` explains it.
 */
export type FlowLeg = {
  asset: string;
  /** Null is "we could not value this", never zero. */
  amountUsd: number | null;
  venue: { project: string; apy: number } | null;
  /** venue_id === IDLE_VENUE_ID: safe, but unplaced and earning nothing. */
  idle: boolean;
  reason?: string;
};

/**
 * Protocol marks, bundled like the token marks and for the same reason: a
 * hotlinked CDN <img> is a network request that fails into a broken image.
 * Keyed by the venue's own `project` slug, so nothing is guessed from a name.
 *
 *   aave-v3      trustwallet/assets ethereum 0x7Fc6…DDaE9 (AAVE) logo.png
 *   compound-v3  trustwallet/assets ethereum 0xc00e…26888 (COMP) logo.png
 *   moonwell     trustwallet/assets base     0xA885…296AE (WELL) logo.png
 *   morpho-blue  cdn.morpho.org/assets/logos/morpho.svg
 *
 * Anything unmapped renders as its label alone. A missing mark is honest; the
 * wrong protocol's mark is not.
 */
const PROTOCOL_MARK: Record<string, string> = {
  "aave-v3": "aave-v3.png",
  "compound-v3": "compound-v3.png",
  "morpho-blue": "morpho-blue.svg",
  moonwell: "moonwell.png",
};

type CardData = {
  [k: string]: unknown;
  title: string;
  /** The figure this node is about: the slice's value, or the venue's rate. */
  value?: string;
  /** Said instead of a value, when there is no value to say. */
  note?: string;
  /** Token mark, by API symbol. */
  symbol?: string;
  /** Protocol mark, by venue `project` slug. */
  project?: string;
  /** The money's origin: the user's own wallet. */
  wallet?: boolean;
  tone?: "idle" | "muted";
  valueTone?: "positive";
  /** Suppresses the handle on the side nothing connects to. */
  ends?: "left" | "right";
};

type CardNode = Node<CardData, "card">;

/** One surface for every box on the diagram. States tint it; none replace it. */
const BASE = "flex h-full items-center gap-2.5 rounded-xl border px-3 py-2.5";

const TONE: Record<string, string> = {
  // Deliberate, not an alarm: a warning-tinted card, not a filled banner.
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
    // Plain <img>: fixed-size marks already at their final dimensions, so the
    // optimizer has nothing to do.
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

// Two lines of content per card now that the figures live inside them, so the
// rows are pitched off the taller box and the columns are measured for the
// longest label each one can hold ("Compound v3", "value unknown").
const ROW = 96;
const WIDTH = { basket: 150, asset: 160, venue: 180 };
const COL = { basket: 0, asset: 200, venue: 410 };

/**
 * Where the money is and how it was divided. A diagram, not a canvas: every
 * interaction is off, the layout is computed, and there is no minimap.
 *
 * Every figure sits inside the node it describes. Edges are bare connectors:
 * a label on the line crowds the line, and a value belongs to the thing it is
 * the value of.
 */
export function BasketFlow({
  legs,
  caption,
  emptyLabel,
}: {
  legs: FlowLeg[];
  /** Says whether these are real positions or a routing plan. */
  caption: string;
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
      // A wallet, not a basket monogram: the money never leaves the user's own
      // wallet, which is the whole point of the architecture.
      data: { title: "Your wallet", wallet: true, ends: "left" },
      style: { width: WIDTH.basket },
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
          // A missing value is stated, not replaced with a number.
          ...(l.amountUsd === null
            ? { note: "value unknown" }
            : { value: fmtUsd(l.amountUsd) }),
          symbol: l.asset,
        },
        style: { width: WIDTH.asset },
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
          style: { width: WIDTH.venue },
          draggable: false,
        });
        edges.push({ id: `e-${asset}-${idle}`, source: asset, target: idle });
        return;
      }

      if (!l.venue) {
        // No venue edge at all: there is nowhere for this slice to go, and a
        // dangling arrow would imply there is.
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
          style: { width: WIDTH.venue },
          draggable: false,
        });
        return;
      }

      const venue = `venue:${l.asset}`;
      nodes.push({
        id: venue,
        type: "card",
        position: { x: COL.venue, y },
        // The protocol's own name, never the internal venue id.
        data: {
          title: displayProject(l.venue.project),
          value: fmtPct(l.venue.apy),
          valueTone: "positive",
          project: l.venue.project,
          ends: "right",
        },
        style: { width: WIDTH.venue },
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
    <div>
      <div style={{ height: Math.max(200, legs.length * ROW + 72) }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
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
      <p className="px-5 pb-4 text-xs text-muted-foreground">{caption}</p>
    </div>
  );
}
