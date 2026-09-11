// Types mirrored from the Go structs they are serialized from. Field names and
// nullability follow the `json:` tags exactly — a pointer in Go is `| null` here.

// --- wallet service: services/wallet/internal/store/models.go ---

/** store.User */
export type Me = {
  id: string;
  privy_did: string;
  wallet_address: string;
  /** Privy's own ID for the embedded wallet. The executor cannot sign without it. */
  privy_wallet_id: string;
  delegated: boolean;
  created_at: string;
};

/** store.Weight */
export type Weight = { asset: string; weight_bps: number };

/** store.Basket */
export type Basket = {
  id: string;
  creator_id: string;
  name: string;
  description: string;
  chain: string;
  is_public: boolean;
  fee_bps: number;
  weights: Weight[] | null;
  created_at: string;
};

/** store.Position */
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

/** executor.Step — absent entirely when nothing was submitted. */
export type Step = {
  step: number;
  tx_hash: string;
  /** confirmed | reverted | pending | submitted | failed */
  outcome: string;
};

/** store.Execution */
export type Execution = {
  id: string;
  user_id: string;
  basket_id?: string | null;
  kind: string;
  asset: string;
  from_venue?: string | null;
  to_venue?: string | null;
  amount_usd: number;
  tx_hash?: string | null;
  status: string;
  error?: string | null;
  steps?: Step[] | null;
  created_at: string;
};

/** store.Subscription */
export type Subscription = {
  id: string;
  user_id: string;
  basket_id: string;
  status: string;
  created_at: string;
};

// --- market data venue: shared by both services ---

/** venue.Venue / marketdata.Venue */
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

/** store.AssetSummary */
export type AssetSummary = {
  asset: string;
  chain: string;
  venues: number;
  best_apy: number;
  best_venue: string;
  total_tvl_usd: number;
};

export type SourceStatus = {
  protocol?: string;
  ok?: boolean;
  venues?: number;
  last_success?: string;
  error?: string;
};

// --- routing plan: api.PlanLeg ---

export type PlanLeg = {
  asset: string;
  weight_bps: number;
  amount_usd: number;
  /** null when the asset has no usable Chainlink feed. Never render a substitute. */
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

// --- portfolio: api.Holding, embeds store.Position ---

export type Holding = Position & {
  current_apy: number;
  best_venue?: Venue;
  drift_apy: number;
  /** null when the holding could not be valued from real data. */
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

// --- execution results: api.LegResult ---

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

/** Positions with this venue mark money sitting unplaced in the user's wallet. */
export const IDLE_VENUE_ID = "idle:wallet";
export const TOTAL_BPS = 10000;
