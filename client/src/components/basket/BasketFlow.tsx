"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import {
  BaseEdge,
  Handle,
  Position,
  Controls,
  ReactFlow,
  ReactFlowProvider,
  getSmoothStepPath,
  useReactFlow,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { CircleDashed, Maximize2, Wallet, X } from "lucide-react";
import {
  QUOTE_ASSET,
  displayProject,
  fmtPct,
  fmtUsd,
  legLabel,
} from "@/lib/api";
import { ProtocolIcon } from "@/components/ProtocolIcon";
import { TokenIcon } from "@/components/TokenIcon";
import type { FlowLeg } from "@/lib/types";

type CardData = {
  [k: string]: unknown;
  title: string;
  value?: string;
  note?: string;
  symbol?: string;
  swapped?: boolean;
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
  if (data.symbol)
    return (
      <span className="relative shrink-0">
        <TokenIcon symbol={data.symbol} size={22} />
        {data.swapped && (
          // eslint-disable-next-line @next/next/no-img-element
          <img
            src="/protocols/uniswap.png"
            alt="swapped on Uniswap"
            width={12}
            height={12}
            className="absolute -bottom-0.5 -right-0.5 size-3 rounded-full ring-2 ring-card"
          />
        )}
      </span>
    );
  if (data.tone === "idle")
    return <CircleDashed className="size-[22px] shrink-0" />;
  if (!data.project) return null;
  return <ProtocolIcon project={data.project} size={22} />;
}

function Card({ data }: NodeProps<CardNode>) {
  const second = data.value ?? data.note;
  return (
    <div
      title={data.note}
      className={`${BASE} ${TONE[data.tone as string] ?? "border-border bg-card"}`}
    >
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
        <Handle
          type="source"
          position={Position.Right}
          className="!opacity-0"
        />
      )}
    </div>
  );
}

function FanEdge({ id, sourceX, sourceY, targetX, targetY, style }: EdgeProps) {
  const [path] = getSmoothStepPath({
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition: Position.Right,
    targetPosition: Position.Left,
    borderRadius: 12,
  });
  return <BaseEdge id={id} path={path} style={style} />;
}

const nodeTypes = { card: Card };
const edgeTypes = { fan: FanEdge };

// Uniform height: unequal heights bow same-row edges.
const ROW = 76;
const HEIGHT = 58;
const WIDTH = { basket: 112, asset: 160, venue: 172 };
const COL = { basket: 0, asset: 168, venue: 348 };

const defaultEdgeOptions = { type: "straight" } as const;

export function BasketFlow({
  legs,
  emptyLabel,
  swapMark,
}: {
  legs: FlowLeg[];
  emptyLabel?: string;
  swapMark?: boolean;
}) {
  const full = useRef<HTMLDialogElement>(null);
  const [expanded, setExpanded] = useState(false);

  const setFull = (open: boolean) => {
    setExpanded(open);
    if (open) full.current?.showModal();
    else full.current?.close();
  };

  const { nodes, edges } = useMemo(() => {
    const nodes: CardNode[] = [];
    const edges: Edge[] = [];
    if (legs.length === 0) return { nodes, edges };

    const mid = ((legs.length - 1) * ROW) / 2;
    nodes.push({
      id: "basket",
      type: "card",
      position: { x: COL.basket, y: mid },
      data: { title: "Wallet", wallet: true, ends: "left" },
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
          title: legLabel(l),
          ...(l.amountUsd !== null
            ? { value: fmtUsd(l.amountUsd) }
            : l.reason
              ? { note: "value unknown" }
              : {}),
          symbol: l.family ?? l.asset,
          swapped: swapMark && l.asset !== QUOTE_ASSET,
        },
        style: { width: WIDTH.asset, height: HEIGHT },
        draggable: false,
      });
      // The funding asset is already in the wallet, so that leg is a deposit,
      // not a swap. Mirrors the executor's own branch.
      edges.push({
        id: `e-basket-${asset}`,
        source: "basket",
        target: asset,
        type: "fan",
      });

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

      if (l.hold) {
        const held = `hold:${l.asset}`;
        nodes.push({
          id: held,
          type: "card",
          position: { x: COL.venue, y },
          data: {
            title: "Your wallet",
            value: fmtPct(0),
            note: l.reason,
            wallet: true,
            ends: "right",
          },
          style: { width: WIDTH.venue, height: HEIGHT },
          draggable: false,
        });
        edges.push({ id: `e-${asset}-${held}`, source: asset, target: held });
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
  }, [legs, swapMark]);

  if (legs.length === 0) {
    if (!emptyLabel) return null;
    return (
      <div className="flex h-44 items-center justify-center px-6 text-center text-sm text-muted-foreground">
        {emptyLabel}
      </div>
    );
  }

  return (
    <>
      <div className="relative">
        <ReactFlowProvider>
          <Diagram nodes={nodes} edges={edges} height={legs.length * ROW + 28} />
        </ReactFlowProvider>
        <button
          type="button"
          onClick={() => setFull(true)}
          aria-label="Expand the routing diagram"
          className="absolute right-3 top-2 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <Maximize2 className="size-4" />
        </button>
      </div>

      <dialog
        ref={full}
        onClose={() => setFull(false)}
        onClick={(e) => e.target === full.current && setFull(false)}
        aria-label="Routing diagram"
        className="m-0 h-screen max-h-none w-screen max-w-none bg-background p-0 text-foreground backdrop:bg-black/80"
      >
        {expanded && (
          <div className="relative h-full w-full">
            <button
              type="button"
              onClick={() => setFull(false)}
              aria-label="Close"
              className="absolute right-4 top-4 z-10 rounded-md p-2 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            >
              <X className="size-5" />
            </button>
            <ReactFlowProvider>
              <Diagram nodes={nodes} edges={edges} expanded />
            </ReactFlowProvider>
          </div>
        )}
      </dialog>
    </>
  );
}

// fitView runs once on mount, and this panel mounts while its column is still
// animating wider: without a refit the diagram stays at the narrow scale.
function Diagram({
  nodes,
  edges,
  height,
  expanded,
}: {
  nodes: CardNode[];
  edges: Edge[];
  height?: number;
  expanded?: boolean;
}) {
  const box = useRef<HTMLDivElement>(null);
  const { fitView } = useReactFlow();

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => {
      void fitView({ padding: 0.08, maxZoom: 1, duration: 150 });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [fitView]);

  return (
    <div
      ref={box}
      className={expanded ? "h-full w-full" : "px-2 pb-3"}
      style={expanded ? undefined : { height }}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        defaultEdgeOptions={defaultEdgeOptions}
        fitView
        fitViewOptions={{ padding: 0.08, maxZoom: 1 }}
        minZoom={0.3}
        maxZoom={expanded ? 2 : 1}
        proOptions={{ hideAttribution: true }}
        nodesDraggable
        nodesConnectable={false}
        elementsSelectable={false}
        panOnDrag
        panOnScroll={false}
        zoomOnScroll={expanded}
        zoomOnPinch={expanded}
        zoomOnDoubleClick={false}
        preventScrolling={false}
      >
        {expanded && <Controls showInteractive={false} className="!shadow-none" />}
      </ReactFlow>
    </div>
  );
}
