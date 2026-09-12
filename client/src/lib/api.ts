import {
  TOTAL_BPS,
  type AssetSummary,
  type BasketSummary,
  type SourceStatus,
  type Venue,
  type Weight,
} from "./types";

export const WALLET_URL =
  process.env.NEXT_PUBLIC_WALLET_URL ?? "http://localhost:8080";
export const MARKET_URL =
  process.env.NEXT_PUBLIC_MARKET_DATA_URL ?? "http://localhost:8081";

// --- chains ---------------------------------------------------------------
// The backend serves Base mainnet and Base Sepolia at the same time. Every
// chain-dependent constant lives in this one table: when the API's chain label
// is confirmed, `label` is the only field that changes.

export type ChainId = 8453 | 84532;

export type ChainInfo = {
  id: ChainId;
  /** The exact string the API expects in `?chain=` and in a basket's `chain`. */
  label: string;
  /** What a human reads. Both networks are Base, so the word is left out. */
  name: string;
  short: string;
  testnet: boolean;
  explorer: string;
  rpc: string;
  /** Canonical USDC, used for the direct balance read fallback. */
  usdc: string;
};

export const CHAINS: readonly ChainInfo[] = [
  {
    id: 8453,
    label: "base",
    name: "Mainnet",
    short: "Mainnet",
    testnet: false,
    explorer: "https://basescan.org",
    rpc: "https://mainnet.base.org",
    usdc: "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
  },
  {
    id: 84532,
    label: "base-sepolia",
    name: "Sepolia",
    short: "Sepolia",
    testnet: true,
    explorer: "https://sepolia.basescan.org",
    rpc: "https://sepolia.base.org",
    usdc: "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
  },
] as const;

export const chainInfo = (id: ChainId): ChainInfo =>
  CHAINS.find((c) => c.id === id) ?? CHAINS[1];

/**
 * Default network, before the user's remembered choice loads. Testnet unless
 * mainnet is configured explicitly: guessing mainnet would point a fresh
 * browser at real money. NEXT_PUBLIC_CHAIN is the older label-shaped setting,
 * still honoured so an existing .env keeps meaning what it meant.
 */
const envChain = process.env.NEXT_PUBLIC_CHAIN_ID ?? process.env.NEXT_PUBLIC_CHAIN;

export const DEFAULT_CHAIN_ID: ChainId =
  envChain === "8453" || envChain === "Base" || envChain === "base"
    ? 8453
    : 84532;

/**
 * The active chain, held at module scope so every call site in this file reads
 * it without threading a parameter. It is an external store: ChainProvider
 * subscribes with useSyncExternalStore, which is what keeps the server render
 * on DEFAULT_CHAIN_ID while the client picks up the remembered choice without
 * a hydration mismatch.
 *
 * Only ChainProvider writes. Never call the setter from a component.
 */
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

/**
 * Do two chain labels name the same network?
 *
 * The backend serves both Base networks from one account, so a list read comes
 * back mixed and the client is what separates them. Case-insensitive, because
 * rows written before the labels were normalised may carry "Base" rather than
 * "base". An empty label matches nothing: showing an unplaceable row on both
 * networks would add a testnet dollar to a real total.
 *
 * Takes the active label rather than reading the store, so a caller inside a
 * memo has an honest dependency on the chain it filtered by.
 */
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

/** Both services report errors as JSON, in two different shapes. */
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

/**
 * Result of a wallet-service call. Status is carried through because 207 is a
 * real outcome for deposit and rebalance, not an error.
 */
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
  // 2xx includes 207 Multi-Status: partial success is a success the caller must
  // inspect leg by leg, never a thrown error.
  if (res.status >= 200 && res.status < 300) {
    return { status: res.status, data: body as T };
  }
  throw new ApiError(res.status, errorMessage(body, res.status));
}

/** market-data wraps every success body in {"data": …}. Missing that is a bug we already paid for once. */
const marketFetch = <T,>(path: string): Promise<T> => envelope<T>(MARKET_URL + path);

/** Unwraps the {"data": …} envelope both services use for public reads. */
async function envelope<T>(url: string): Promise<T> {
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
  return (body as { data: T }).data;
}

/** Same as envelope(), for a public read that is not wrapped in {"data": …}. */
async function plainJson<T>(url: string): Promise<T> {
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

/**
 * Public basket browsing — no Authorization header, no session. A visitor must
 * be able to see what is on offer before connecting anything. These responses
 * carry no `subscribed` flag: it is per-caller and meaningless anonymously, so
 * an authenticated view uses GET /v1/baskets?scope=public instead.
 */
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

export const market = {
  assets: (chain = activeChain().label) =>
    marketFetch<{ assets: AssetSummary[] | null; count: number }>(
      `/assets?chain=${encodeURIComponent(chain)}`,
    ),
  venues: (asset: string, chain = activeChain().label, limit = 10) =>
    marketFetch<{ venues: Venue[] | null; count: number }>(
      `/venues?chain=${encodeURIComponent(chain)}&asset=${encodeURIComponent(asset)}&limit=${limit}`,
    ),
  sources: () =>
    marketFetch<{ sources: SourceStatus[] | null; count: number }>("/sources"),
};

// --- derived figures ---

/**
 * A basket's blended APY, from its weights against the per-asset best venue
 * that GET /assets already summarises. One request for the whole list, not one
 * /plan call per card.
 *
 * Returns null — never 0 — when any weighted asset has no venue: a partial
 * blend silently understates the basket, and a zero is a number we invented.
 * The backend planner applies a TVL floor this summary does not, so treat the
 * result as the same figure within that floor's margin, not a quote.
 */
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

// --- formatting: never invent a number ---

export const fmtUsd = (n: number) =>
  n.toLocaleString("en-US", {
    style: "currency",
    currency: "USD",
    maximumFractionDigits: n !== 0 && Math.abs(n) < 1 ? 4 : 2,
  });

/** A null value is unknown, not zero. Callers must render the reason next to it. */
export const fmtUsdOrUnknown = (n: number | null | undefined) =>
  n === null || n === undefined ? "value unknown" : fmtUsd(n);

export const fmtPct = (n: number, digits = 2) => `${n.toFixed(digits)}%`;

/** A null APY is uncomputable, not zero. */
export const fmtPctOrDash = (n: number | null | undefined, digits = 2) =>
  n === null || n === undefined ? "—" : fmtPct(n, digits);

export const fmtBps = (bps: number) => `${(bps / 100).toFixed(2)}%`;

export const fmtCompactUsd = (n: number) =>
  `$${n.toLocaleString("en-US", { notation: "compact", maximumFractionDigits: 1 })}`;

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

// --- asset display + allocation ---

/**
 * Display-only symbol map. The wrapped-token name is an implementation detail
 * of the chain, not something a depositor asked for. Everything sent to the API
 * keeps the real symbol — this is applied at the render layer only.
 */
const DISPLAY_ASSET: Record<string, string> = { WETH: "ETH" };

/**
 * The only asset anyone deposits. A basket's tokens are what this is converted
 * into — a different concept, and never the same control.
 */
export const QUOTE_ASSET = "USDC";

/** One entry of the executor's swap allowlist, as GET /public/swap-paths. */
export type SwapPath = {
  chain_id: number;
  from: string;
  to: string;
  hops: number;
};

/**
 * The executor's swap allowlist. Unauthenticated, and not wrapped in the
 * {"data": …} envelope the other public reads use.
 */
export const swapPaths = () =>
  plainJson<{ quote_asset: string; count: number; paths: SwapPath[] | null }>(
    `${WALLET_URL}/public/swap-paths`,
  );

/**
 * What a USDC deposit can actually be converted into on `chainId`: the quote
 * asset itself, plus every allowlisted target.
 */
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

/**
 * Can a USDC deposit acquire this asset?
 *
 * `targets` null means the allowlist could not be read — a 503 or a network
 * error, not an empty allowlist. Unknown is answered `true`: a transient
 * failure must not grey out every token on the page and tell the user, wrongly,
 * that nothing can be bought. The deposit itself is still refused executor-side
 * if the path really is missing, so the honest failure is preserved either way.
 */
export function isSwappable(
  asset: string,
  targets: ReadonlySet<string> | null,
): boolean {
  return targets === null || targets.has(asset);
}

export const displayAsset = (symbol: string) => DISPLAY_ASSET[symbol] ?? symbol;

/**
 * Venue `project` slug -> the protocol's own name. Display only: the slug is
 * what the API is given and what a mark is resolved by, and an unknown slug is
 * shown verbatim rather than prettified into something nobody deployed.
 */
const DISPLAY_PROJECT: Record<string, string> = {
  "aave-v3": "Aave v3",
  "compound-v3": "Compound v3",
  "morpho-blue": "Morpho Blue",
  moonwell: "Moonwell",
};

export const displayProject = (project: string) =>
  DISPLAY_PROJECT[project] ?? project;

/**
 * Even split across assets, summing to exactly `total` (bps by default;
 * pass 100 to get the whole-percent readout the UI shows).
 *
 * Mirrors allocate() in services/wallet/internal/api/deposit.go: equal weights
 * give every largest-remainder fraction the same value, so its stable sort
 * leaves them in original order and the leftovers land on the first legs. Three
 * assets is 3334/3333/3333 — not 3333×3, which sums to 9999 and the backend
 * rejects.
 */
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

// --- direct balance read --------------------------------------------------

/**
 * Native and USDC balance straight from a public Base RPC.
 *
 * Used only as the fallback for the wallet menu: GET /v1/portfolio already
 * carries onchain ERC-20 balances, but only when TOKEN_API_JWT is configured,
 * and it never carries native ETH because the Token API indexes ERC-20s only.
 *
 * A failed read returns null for that leg. Null is rendered as a dash with a
 * reason — never as zero, which reads as "you have no money".
 */
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

/** hex wei/units -> decimal number. BigInt first so 1e18 does not lose the low bits. */
const scale = (hex: string, decimals: number): number =>
  Number(BigInt(hex || "0x0")) / 10 ** decimals;

export async function readBalances(
  address: string,
  id: ChainId = activeChainId(),
): Promise<WalletBalances> {
  const c = chainInfo(id);
  // balanceOf(address) = 0x70a08231 + the address left-padded to 32 bytes.
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

/** Token amounts, not dollars: a balance is a quantity until a price says otherwise. */
export const fmtToken = (n: number, digits = 4) =>
  n.toLocaleString("en-US", { maximumFractionDigits: digits });
