package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/internal/shared/prices"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/tokenapi"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{"status": "ok", "db": "up", "executor": "up"}
	if err := s.Store.Ping(r.Context()); err != nil {
		out["status"], out["db"] = "degraded", "down"
	}
	if err := s.Executor.Health(r.Context()); err != nil {
		out["status"], out["executor"] = "degraded", "down"
	}
	writeJSON(w, http.StatusOK, out)
}

// --- me ---

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// upsertMe binds the caller's Privy DID to their embedded wallet address.
// The client calls this once after login, and again after granting delegation.
func (s *Server) upsertMe(w http.ResponseWriter, r *http.Request) {
	c, ok := auth.FromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		WalletAddress string `json:"wallet_address"`
		PrivyWalletID string `json:"privy_wallet_id"`
		Delegated     bool   `json:"delegated"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.WalletAddress == "" {
		writeErr(w, http.StatusBadRequest, "wallet_address required")
		return
	}
	if msg := validPrivyWalletID(body.PrivyWalletID); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	u, err := s.Store.UpsertUser(r.Context(), c.DID, body.WalletAddress, body.PrivyWalletID)
	if err != nil {
		s.Log.Error("upsert user", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if body.Delegated != u.Delegated {
		if err := s.Store.SetDelegated(r.Context(), u.ID, body.Delegated); err != nil {
			s.Log.Error("set delegated", "err", err)
		}
		u.Delegated = body.Delegated
	}
	writeJSON(w, http.StatusOK, u)
}

// validPrivyWalletID returns "" when id is acceptable, or the reason it is not.
//
// The wallet ID and the wallet address are both strings read off the same Privy
// object, so posting the address here typechecks, stores cleanly, and only
// fails later as an opaque 404 from Privy — after the user has deposited and is
// waiting on a transaction that will never exist. Catching it at the edge is
// the difference between a 400 and a stuck deposit.
//
// Observed format: 24 lowercase alphanumeric characters, no prefix, e.g.
// "dxvzlpuqjfr6iclupqssmo4a".
func validPrivyWalletID(id string) string {
	// Empty is legitimate: Privy leaves the server wallet ID null until the
	// wallet is delegated, so the first POST /v1/me cannot carry one.
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "0x") || strings.HasPrefix(id, "0X") {
		return "privy_wallet_id looks like a wallet address; this field wants the Privy wallet ID " +
			"(24 lowercase alphanumeric characters, e.g. dxvzlpuqjfr6iclupqssmo4a), " +
			"not the 0x address — that goes in wallet_address"
	}
	if len(id) != privyWalletIDLen {
		return fmt.Sprintf("privy_wallet_id must be %d lowercase alphanumeric characters, got %d",
			privyWalletIDLen, len(id))
	}
	// A character-class loop beats a regexp here: clearer, and nothing to import.
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return "privy_wallet_id must be 24 lowercase alphanumeric characters"
		}
	}
	return ""
}

const privyWalletIDLen = 24

// --- baskets ---

func (s *Server) createBasket(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Chain       string         `json:"chain"`
		IsPublic    bool           `json:"is_public"`
		FeeBps      int            `json:"fee_bps"`
		Weights     []store.Weight `json:"weights"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if body.Chain == "" {
		body.Chain = s.DefaultChain
	}
	// Store the canonical label: venue ids, the market-data API and the
	// executor allowlist all key on it, and "Base " or "BASE" would route
	// nowhere at deposit time instead of failing here, in the user's face.
	canonical, ok := chains.Normalize(body.Chain)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unsupported chain "+body.Chain)
		return
	}
	body.Chain = canonical
	b, err := s.Store.CreateBasket(r.Context(), store.Basket{
		CreatorID:   u.ID,
		Name:        body.Name,
		Description: body.Description,
		Chain:       body.Chain,
		IsPublic:    body.IsPublic,
		FeeBps:      body.FeeBps,
		Weights:     body.Weights,
	})
	if err != nil {
		// Weight-sum and duplicate-asset failures are the caller's fault.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// ownerView stamps the two per-caller flags onto a basket. They are
// independent: a creator usually subscribes to their own basket too, and a
// basket someone made but has not joined must not read as "not joined".
// creator_id itself stays where it is — the boolean is the whole answer.
func ownerView(b store.Basket, userID string, subscribed bool) store.Basket {
	b.Subscribed = subscribed
	b.CreatedByMe = b.CreatorID == userID
	return b
}

func (s *Server) listBaskets(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	var owner string
	if r.URL.Query().Get("scope") == "mine" {
		owner = u.ID
	}
	bs, err := s.Store.ListBaskets(r.Context(), owner, owner == "", limit)
	if err != nil {
		s.Log.Error("list baskets", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// One query for the whole page, not one per basket.
	ids := make([]string, len(bs))
	for i := range bs {
		ids[i] = bs[i].ID
	}
	subs, err := s.Store.SubscribedTo(r.Context(), u.ID, ids)
	if err != nil {
		s.Log.Warn("subscribed lookup", "err", err)
	}
	for i := range bs {
		bs[i] = ownerView(bs[i], u.ID, subs[bs[i].ID])
	}
	writeJSON(w, http.StatusOK, bs)
}

func (s *Server) getBasket(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	b, err := s.Store.Basket(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "basket not found")
		return
	}
	if err != nil {
		s.Log.Error("get basket", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// The client needs to know whether to offer Join or Exit; without this it
	// has to show both.
	subs, err := s.Store.SubscribedTo(r.Context(), u.ID, []string{b.ID})
	if err != nil {
		s.Log.Warn("subscribed lookup", "err", err)
	}
	writeJSON(w, http.StatusOK, ownerView(b, u.ID, subs[b.ID]))
}

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	// Exactly the preconditions deposit enforces. Subscribing without them
	// would put the user in a state that can never execute, and they would not
	// find out until their first deposit failed.
	if !s.canExecute(w, u) {
		return
	}
	sub, err := s.Store.Subscribe(r.Context(), u.ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot subscribe")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if err := s.Store.Unsubscribe(r.Context(), u.ID, r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "exited"})
}

// --- routing plan ---

// PlanLeg is one asset's share of a basket and the venue it would be routed to.
type PlanLeg struct {
	Asset     string  `json:"asset"`
	WeightBps int     `json:"weight_bps"`
	AmountUSD float64 `json:"amount_usd"`
	// PriceUSD and AmountToken are nil when the asset has no usable price feed.
	// The leg is then unroutable rather than valued at a guess.
	PriceUSD    *float64          `json:"price_usd"`
	AmountToken *float64          `json:"amount_token"`
	Venue       *marketdata.Venue `json:"venue,omitempty"`
	Reason      string            `json:"reason"`
}

// planBasket resolves a basket's weights against live venue data and returns
// where each slice of a deposit would go. Read-only — it signs nothing. This is
// what the client shows the user before they approve a deposit.
func (s *Server) planBasket(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.caller(w, r); !ok {
		return
	}
	b, err := s.Store.Basket(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "basket not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The parse error is deliberately ignored: a missing or malformed
	// amount_usd plans $0, which is exactly what the client asks for when it
	// renders a basket's allocation with no amount entered yet. Do not "fix"
	// this into a 400 — that breaks the zero-amount preview.
	amount, _ := strconv.ParseFloat(r.URL.Query().Get("amount_usd"), 64)
	legs, blended := s.planLegs(r, b, amount)

	writeJSON(w, http.StatusOK, map[string]any{
		"basket_id":   b.ID,
		"chain":       b.Chain,
		"amount_usd":  amount,
		"blended_apy": blended,
		"legs":        legs,
	})
}

// --- portfolio ---

// Holding is a live position plus the drift between where it sits and where it
// should sit. Drift is the rebalance signal.
type Holding struct {
	store.Position
	CurrentAPY float64           `json:"current_apy"`
	BestVenue  *marketdata.Venue `json:"best_venue,omitempty"`
	DriftAPY   float64           `json:"drift_apy"`
	// OnchainUSD is nil when the holding could not be valued from real data —
	// never a placeholder. ValueReason then says why.
	OnchainUSD  *float64 `json:"onchain_usd"`
	Reconciled  bool     `json:"reconciled"`
	ValueReason string   `json:"value_reason,omitempty"`
	// RouteNote names a better-but-unexecutable venue when one exists.
	RouteNote string `json:"route_note,omitempty"`
}

// driftAPY compares a position against the best venue available now.
// Same venue: the entry rate is stale, the live rate is the truth and there is
// nothing to move. Different venue: drift is what we would gain by moving.
func driftAPY(p store.Position, best marketdata.Venue) (current, drift float64) {
	if best.ID == p.VenueID {
		return best.APY, 0
	}
	return p.EntryAPY, best.APY - p.EntryAPY
}

func (s *Server) portfolio(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	positions, err := s.Store.Positions(r.Context(), u.ID, r.URL.Query().Get("basket_id"))
	if err != nil {
		s.Log.Error("positions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	network := tokenAPINetwork(s.DefaultChain)
	balances, onchainErr := s.TokenAPI.Balances(r.Context(), u.WalletAddress, network, 100)
	if onchainErr != nil && !errors.Is(onchainErr, tokenapi.ErrNotConfigured) {
		// Onchain truth is a bonus view, never a reason to fail the endpoint.
		s.Log.Warn("token api balances", "err", onchainErr)
	}

	holdings := make([]Holding, 0, len(positions))
	var totalUSD, weightedAPY float64
	for _, p := range positions {
		h := Holding{Position: p, CurrentAPY: p.EntryAPY}
		// The best venue shown here is the best *executable* one, so the drift a
		// user sees is drift they can actually act on. A better rate we cannot
		// reach is named in route_note rather than hidden or promised.
		if best, note, err := s.bestRoutable(r.Context(), p.Asset, p.Chain); err == nil {
			h.BestVenue = &best
			h.RouteNote = note
			h.CurrentAPY, h.DriftAPY = driftAPY(p, best)
		}
		if onchainErr == nil {
			h.OnchainUSD, h.Reconciled, h.ValueReason = s.reconcile(r.Context(), p, balances)
		}
		totalUSD += p.AmountUSD
		weightedAPY += h.CurrentAPY * p.AmountUSD
		holdings = append(holdings, h)
	}
	if totalUSD > 0 {
		weightedAPY /= totalUSD
	}

	out := map[string]any{
		"wallet_address":    u.WalletAddress,
		"total_usd":         totalUSD,
		"blended_apy":       weightedAPY,
		"positions":         holdings,
		"onchain_available": onchainErr == nil,
	}
	if onchainErr == nil {
		out["onchain"] = map[string]any{
			"chain":       network,
			"token_count": len(balances),
			"balances":    balances,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// reconcile values the wallet's onchain balance of a position's asset against
// what our own books claim.
//
// Only an exact symbol match is reconciled. A position sitting in a venue is
// usually held as a receipt token (aBasUSDC, mUSDC, …) whose redemption rate we
// do not read onchain — guessing it is 1:1 would be inventing a number, so
// those report unreconciled with a reason instead.
//
// Value comes from the asset's Chainlink feed. No feed, or a stale one, means
// no value: nil, not a fabricated dollar.
func (s *Server) reconcile(ctx context.Context, p store.Position, balances []tokenapi.Balance) (onchainUSD *float64, ok bool, reason string) {
	asset := strings.ToUpper(p.Asset)
	var tokens float64
	found := false
	for _, b := range balances {
		if strings.ToUpper(b.Symbol) == asset {
			tokens += b.Value
			found = true
		}
	}
	if !found {
		return nil, false, "no matching onchain balance; the position is likely held as a venue receipt token"
	}
	price, err := s.priceUSD(ctx, p.Chain, p.Asset)
	if err != nil {
		return nil, false, priceReason(p.Asset, err)
	}
	usd := tokens * price.USD
	tolerance := math.Max(1, p.AmountUSD*0.02) // 2% or a dollar, whichever is bigger
	return &usd, math.Abs(usd-p.AmountUSD) <= tolerance, ""
}

// tokenAPINetwork maps our chain label onto the Token API's network name. The
// Token API indexes Base mainnet only, so a Sepolia portfolio simply has no
// onchain view — reported as unavailable rather than filled from mainnet.
func tokenAPINetwork(chain string) string {
	if id, ok := chains.ID(chain); ok && id == chains.BaseSepolia {
		return "base-sepolia"
	}
	return "base"
}

// priceReason turns a pricing failure into something a user can act on.
func priceReason(asset string, err error) string {
	switch {
	case errors.Is(err, prices.ErrNoChain):
		return "this basket's chain is not configured for pricing; value unknown"
	case errors.Is(err, prices.ErrNoFeed):
		return "no Chainlink price feed for " + asset + " on this chain; value unknown"
	case errors.Is(err, prices.ErrStale):
		return "the " + asset + " price feed is stale; value unknown"
	default:
		return "could not read the " + asset + " price feed; value unknown"
	}
}

func (s *Server) executions(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	es, err := s.Store.Executions(r.Context(), u.ID, limit)
	if err != nil {
		s.Log.Error("executions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, es)
}
