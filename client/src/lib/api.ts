import type { AssetSummary, SourceStatus, Venue } from "./types";

export const WALLET_URL =
  process.env.NEXT_PUBLIC_WALLET_URL ?? "http://localhost:8080";
export const MARKET_URL =
  process.env.NEXT_PUBLIC_MARKET_DATA_URL ?? "http://localhost:8081";
export const CHAIN = process.env.NEXT_PUBLIC_CHAIN ?? "Base";

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
async function marketFetch<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(MARKET_URL + path);
  } catch (e) {
    throw new ApiError(
      0,
      `cannot reach market-data at ${MARKET_URL} (${
        e instanceof Error ? e.message : "network error"
      })`,
    );
  }
  const body = await readBody(res);
  if (!res.ok) throw new ApiError(res.status, errorMessage(body, res.status));
  return (body as { data: T }).data;
}

export const market = {
  assets: (chain = CHAIN) =>
    marketFetch<{ assets: AssetSummary[] | null; count: number }>(
      `/assets?chain=${encodeURIComponent(chain)}`,
    ),
  venues: (asset: string, chain = CHAIN, limit = 10) =>
    marketFetch<{ venues: Venue[] | null; count: number }>(
      `/venues?chain=${encodeURIComponent(chain)}&asset=${encodeURIComponent(asset)}&limit=${limit}`,
    ),
  sources: () =>
    marketFetch<{ sources: SourceStatus[] | null; count: number }>("/sources"),
};

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

export const fmtBps = (bps: number) => `${(bps / 100).toFixed(2)}%`;

export const fmtCompactUsd = (n: number) =>
  `$${n.toLocaleString("en-US", { notation: "compact", maximumFractionDigits: 1 })}`;

export const shortHash = (h: string) =>
  h.length > 14 ? `${h.slice(0, 8)}…${h.slice(-6)}` : h;

export const basescanTx = (hash: string) => `https://basescan.org/tx/${hash}`;

export const basescanAddress = (addr: string) =>
  `https://basescan.org/address/${addr}`;

export const fmtTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
};
