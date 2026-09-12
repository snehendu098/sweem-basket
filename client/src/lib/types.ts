
export type Me = {
  id: string;
  privy_did: string;
  wallet_address: string;
  privy_wallet_id: string;
  delegated: boolean;
  created_at: string;
};

export type Weight = { asset: string; weight_bps: number };

export type BasketSummary = {
  id: string;
  name: string;
  description: string;
  chain: string;
  fee_bps: number;
  weights: Weight[] | null;
  created_at: string;
  subscribed?: boolean;
  created_by_me?: boolean;
};

export type Basket = BasketSummary & {
  creator_id: string;
  is_public: boolean;
  subscribed: boolean;
};

export function ownership(b: BasketSummary): string | null {
  if (b.created_by_me === undefined && b.subscribed === undefined) return null;
  if (b.created_by_me) return b.subscribed ? "created · joined" : "created";
  return b.subscribed ? "joined" : "not joined";
}

export type Position = {
  id: string;
  user_id: string;
  basket_id: string;
  asset: string;
  venue_id: string;
  chain: string;
  project: string;
  amount_usd: number;
  entry_apy: number;
  updated_at: string;
};

export type Step = {
  step: number;
  tx_hash: string;
  outcome: string;
};

export type Venue = {
  id: string;
  chain: string;
  project: string;
  symbol: string;
  pool_id?: string;
  asset: string;
  tvl_usd: number;
  apy: number;
  apy_base: number;
  apy_reward: number;
  stablecoin: boolean;
  updated_at: string;
};

export type AssetSummary = {
  asset: string;
  chain: string;
  venues: number;
  best_apy: number;
  best_venue: string;
  total_tvl_usd: number;
};

export type PlanLeg = {
  asset: string;
  weight_bps: number;
  amount_usd: number;
  price_usd: number | null;
  amount_token: number | null;
  venue?: Venue;
  reason: string;
};

export type Plan = {
  basket_id: string;
  chain: string;
  amount_usd: number;
  blended_apy: number;
  legs: PlanLeg[] | null;
};

export type Holding = Position & {
  current_apy: number;
  best_venue?: Venue;
  drift_apy: number;
  onchain_usd: number | null;
  reconciled: boolean;
  value_reason?: string;
};

export type OnchainBalance = {
  contract: string;
  symbol: string;
  decimals: number;
  amount: string;
  value: number;
  network: string;
};

export type Portfolio = {
  wallet_address: string;
  total_usd: number;
  blended_apy: number;
  positions: Holding[] | null;
  onchain_available: boolean;
  onchain?: { chain: string; token_count: number; balances: OnchainBalance[] | null };
};

export type LegStatus = "submitted" | "pending" | "failed" | "skipped";

export type LegResult = {
  asset: string;
  amount_usd: number;
  from_venue_id?: string;
  venue_id?: string;
  project?: string;
  apy?: number;
  execution_id?: string;
  tx_hash?: string;
  status: LegStatus;
  reason?: string;
  steps?: Step[];
};

export type SettleResult = {
  basket_id: string;
  amount_usd?: number;
  submitted_usd?: number;
  threshold_apy?: number;
  moved_legs?: number;
  failed_legs: number;
  pending_legs: number;
  legs: LegResult[] | null;
};

export const IDLE_VENUE_ID = "idle:wallet";
export const TOTAL_BPS = 10000;
