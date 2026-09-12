import {
  TOTAL_BPS,
  type AssetSummary,
  type BasketSummary,
  type Weight,
} from "./types";

export const WALLET_URL =
  process.env.NEXT_PUBLIC_WALLET_URL ?? "http://localhost:8080";
export const MARKET_URL =
  process.env.NEXT_PUBLIC_MARKET_DATA_URL ?? "http://localhost:8081";

export type ChainId = 8453 | 84532;

export type ChainInfo = {
  id: ChainId;
  label: string;
  name: string;
  testnet: boolean;
  explorer: string;
  rpc: string;
  usdc: string;
};

export const CHAINS: readonly ChainInfo[] = [
  {
    id: 8453,
    label: "base",
    name: "Mainnet",
    testnet: false,
    explorer: "https://basescan.org",
    rpc: "https://mainnet.base.org",
    usdc: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
  },
  {
    id: 84532,
    label: "base-sepolia",
    name: "Sepolia",
    testnet: true,
    explorer: "https://sepolia.basescan.org",
    rpc: "https://sepolia.base.org",
    usdc: "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
  },
] as const;

export const chainInfo = (id: ChainId): ChainInfo =>
  CHAINS.find((c) => c.id === id) ?? CHAINS[1];

// NEXT_PUBLIC_* is baked in at build time — a dev-server restart is not enough.
const envChain = process.env.NEXT_PUBLIC_CHAIN_ID ?? process.env.NEXT_PUBLIC_CHAIN;

export const DEFAULT_CHAIN_ID: ChainId =
  envChain === "8453" || envChain === "Base" || envChain === "base"
    ? 8453
    : 84532;

let active: ChainId = DEFAULT_CHAIN_ID;
const chainListeners = new Set<() => void>();

export const activeChainId = (): ChainId => active;
export const activeChain = (): ChainInfo => chainInfo(active);

export function subscribeChain(fn: () => void): () => void {
  chainListeners.add(fn);
  return () => {
    chainListeners.delete(fn);
  };
}

export function setActiveChainId(id: ChainId) {
  if (id === active) return;
  active = id;
  for (const fn of chainListeners) fn();
}

// Label arg, not the module store: reading it makes exhaustive-deps drop the dep.
export const sameChain = (
  a: string | null | undefined,
  b: string | null | undefined,
): boolean => !!a && !!b && a.toLowerCase() === b.toLowerCase();

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function errorMessage(body: unknown, status: number): string {
  if (body && typeof body === "object" && "error" in body) {
    const e = (body as { error: unknown }).error;
    if (typeof e === "string") return e;
    if (e && typeof e === "object" && "message" in e) {
      return String((e as { message: unknown }).message);
    }
  }
  return `request failed with status ${status}`;
}

async function readBody(res: Response): Promise<unknown> {
  const text = await res.text();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

export type Res<T> = { status: number; data: T };

export async function walletFetch<T>(
  path: string,
  token: string | null,
  init?: RequestInit,
): Promise<Res<T>> {
  let res: Response;
  try {
    res = await fetch(WALLET_URL + path, {
      ...init,
      headers: {
        ...(init?.body ? { "Content-Type": "application/json" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
        ...init?.headers,
      },
    });
  } catch (e) {
    throw new ApiError(
      0,
      `cannot reach the wallet service at ${WALLET_URL} (${
        e instanceof Error ? e.message : "network error"
      })`,
    );
  }
  const body = await readBody(res);
  if (res.status >= 200 && res.status < 300) {
    return { status: res.status, data: body as T };
  }
  throw new ApiError(res.status, errorMessage(body, res.status));
}

async function getJson<T>(url: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(url);
  } catch (e) {
    throw new ApiError(
      0,
      `cannot reach ${url} (${e instanceof Error ? e.message : "network error"})`,
    );
  }
  const body = await readBody(res);
  if (!res.ok) throw new ApiError(res.status, errorMessage(body, res.status));
  return body as T;
}

const envelope = <T,>(url: string): Promise<T> =>
  getJson<{ data: T }>(url).then((b) => b.data);

function unwrapBaskets(d: unknown): BasketSummary[] {
  if (Array.isArray(d)) return d as BasketSummary[];
  if (d && typeof d === "object") {
    const inner = (d as { baskets?: unknown }).baskets;
    if (Array.isArray(inner)) return inner as BasketSummary[];
  }
  return [];
}

export const publicBaskets = () =>
  envelope<unknown>(`${WALLET_URL}/public/baskets`).then(unwrapBaskets);

export const publicBasket = (id: string) =>
  envelope<BasketSummary>(`${WALLET_URL}/public/baskets/${encodeURIComponent(id)}`);

export const marketAssets = (chain = activeChain().label) =>
  envelope<{ assets: AssetSummary[] | null; count: number }>(
    `${MARKET_URL}/assets?chain=${encodeURIComponent(chain)}`,
  );

export function blendApy(
  weights: Weight[] | null | undefined,
  assets: AssetSummary[] | null | undefined,
): number | null {
  if (!weights || weights.length === 0 || !assets) return null;
  let apy = 0;
  for (const w of weights) {
    const a = assets.find((x) => x.asset === w.asset);
    if (!a) return null;
    apy += (a.best_apy * w.weight_bps) / TOTAL_BPS;
  }
  return apy;
}

export const fmtUsd = (n: number) =>
  n.toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: n !== 0 && Math.abs(n) < 1 ? 4 : 2,
  });

export const fmtUsdOrUnknown = (n: number | null | undefined) =>
  n === null || n === undefined ? "value unknown" : fmtUsd(n);

export const fmtPct = (n: number, digits = 2) => `${n.toFixed(digits)}%`;

export const fmtPctOrDash = (n: number | null | undefined, digits = 2) =>
  n === null || n === undefined ? "—" : fmtPct(n, digits);

export const fmtBps = (bps: number) => `${(bps / 100).toFixed(2)}%`;

export const shortHash = (h: string) =>
  h.length > 14 ? `${h.slice(0, 8)}…${h.slice(-6)}` : h;

export const basescanTx = (hash: string) =>
  `${activeChain().explorer}/tx/${hash}`;

export const basescanAddress = (addr: string) =>
  `${activeChain().explorer}/address/${addr}`;

export const fmtTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
};

const DISPLAY_ASSET: Record<string, string> = { WETH: "ETH" };

export const QUOTE_ASSET = "USDC";

export type SwapPath = {
  chain_id: number;
  from: string;
  to: string;
  hops: number;
};

export const swapPaths = () =>
  getJson<{ quote_asset: string; count: number; paths: SwapPath[] | null }>(
    `${WALLET_URL}/public/swap-paths`,
  );

export function swappableAssets(
  res: { quote_asset: string; paths: SwapPath[] | null },
  chainId: ChainId = activeChainId(),
): Set<string> {
  const out = new Set<string>([res.quote_asset]);
  for (const p of res.paths ?? []) {
    if (p.chain_id === chainId && p.from === res.quote_asset) out.add(p.to);
  }
  return out;
}

// Null targets = unknown, not empty: unknown answers true.
export function isSwappable(
  asset: string,
  targets: ReadonlySet<string> | null,
): boolean {
  return targets === null || targets.has(asset);
}

export const displayAsset = (symbol: string) => DISPLAY_ASSET[symbol] ?? symbol;

const DISPLAY_PROJECT: Record<string, string> = {
  "aave-v3": "Aave v3",
  "compound-v3": "Compound v3",
  "morpho-blue": "Morpho Blue",
  moonwell: "Moonwell",
};

export const displayProject = (project: string) =>
  DISPLAY_PROJECT[project] ?? project;

// Largest-remainder, mirroring allocate() in services/wallet/internal/api/deposit.go.
// Three assets is 3334/3333/3333 — 3333×3 sums to 9999, which the backend rejects.
export function evenSplit(assets: string[], total = TOTAL_BPS): Weight[] {
  const n = assets.length;
  if (n === 0) return [];
  const base = Math.floor(total / n);
  const leftover = total - base * n;
  return assets.map((asset, i) => ({
    asset,
    weight_bps: base + (i < leftover ? 1 : 0),
  }));
}

export type WalletBalances = { eth: number | null; usdc: number | null };

async function rpc(url: string, method: string, params: unknown[]): Promise<string> {
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method, params }),
  });
  if (!res.ok) throw new ApiError(res.status, `rpc ${method} failed`);
  const body = (await res.json()) as { result?: string; error?: { message?: string } };
  if (body.error) throw new ApiError(0, body.error.message ?? `rpc ${method} failed`);
  if (typeof body.result !== "string") throw new ApiError(0, `rpc ${method}: no result`);
  return body.result;
}

const scale = (hex: string, decimals: number): number =>
  Number(BigInt(hex || "0x0")) / 10 ** decimals;

export async function readBalances(
  address: string,
  id: ChainId = activeChainId(),
): Promise<WalletBalances> {
  const c = chainInfo(id);
  const data = `0x70a08231${address.toLowerCase().replace(/^0x/, "").padStart(64, "0")}`;
  const [eth, usdc] = await Promise.all([
    rpc(c.rpc, "eth_getBalance", [address, "latest"])
      .then((h) => scale(h, 18))
      .catch(() => null),
    rpc(c.rpc, "eth_call", [{ to: c.usdc, data }, "latest"])
      .then((h) => scale(h, 6))
      .catch(() => null),
  ]);
  return { eth, usdc };
}

export const fmtToken = (n: number, digits = 4) =>
  n.toLocaleString("en-US", { maximumFractionDigits: digits });
