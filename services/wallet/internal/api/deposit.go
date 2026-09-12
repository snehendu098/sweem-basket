package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

func allocate(totalCents int64, weights []int64) []int64 {
	cents := make([]int64, len(weights))
	var totalWeight int64
	for _, w := range weights {
		if w > 0 {
			totalWeight += w
		}
	}
	if totalCents <= 0 || totalWeight <= 0 {
		return cents
	}

	type rem struct {
		i    int
		frac int64
	}
	rems := make([]rem, 0, len(weights))
	var assigned int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		n := totalCents * w
		cents[i] = n / totalWeight
		assigned += cents[i]
		rems = append(rems, rem{i: i, frac: n % totalWeight})
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := int64(0); k < totalCents-assigned && len(rems) > 0; k++ {
		cents[rems[k%int64(len(rems))].i]++
	}
	return cents
}

func centsToUSD(cents []int64) []float64 {
	out := make([]float64, len(cents))
	for i, c := range cents {
		out[i] = float64(c) / 100
	}
	return out
}

func splitAmounts(amountUSD float64, ws []store.Weight) []float64 {
	weights := make([]int64, len(ws))
	for i, w := range ws {
		weights[i] = int64(w.WeightBps)
	}
	return centsToUSD(allocate(int64(math.Round(amountUSD*100)), weights))
}

func splitProportional(amountUSD float64, values []float64) []float64 {
	weights := make([]int64, len(values))
	for i, v := range values {
		weights[i] = int64(math.Round(v * 100))
	}
	return centsToUSD(allocate(int64(math.Round(amountUSD*100)), weights))
}

func (s *Server) planLegs(r *http.Request, b store.Basket, amountUSD float64) ([]PlanLeg, float64) {
	amounts := splitAmounts(amountUSD, b.Weights)
	legs := make([]PlanLeg, 0, len(b.Weights))
	var blended float64
	for i, weight := range b.Weights {
		leg := PlanLeg{
			Asset:     weight.Asset,
			WeightBps: weight.WeightBps,
			AmountUSD: amounts[i],
		}
		price, perr := s.priceUSD(r.Context(), b.Chain, weight.Asset)
		if perr != nil {
			leg.Reason = priceReason(weight.Asset, perr)
			legs = append(legs, leg)
			continue
		}
		leg.PriceUSD = &price.USD
		if price.USD > 0 {
			tokens := leg.AmountUSD / price.USD
			leg.AmountToken = &tokens
		}

		if serr := s.swapFundable(r.Context(), weight.Asset, b.Chain); serr != nil {
			if errors.Is(serr, ErrNoSwapPath) {
				leg.Reason = fmt.Sprintf("no %s swap route to %s on %s; this leg cannot be funded",
					QuoteAsset, weight.Asset, b.Chain)
			} else {
				s.Log.Warn("swap path lookup", "asset", weight.Asset, "err", serr)
				leg.Reason = "executor swap routes unavailable; this leg cannot be funded"
			}
			legs = append(legs, leg)
			continue
		}

		v, note, err := s.bestRoutable(r.Context(), weight.Asset, b.Chain)
		switch {
		case errors.Is(err, marketdata.ErrNoVenue):
			leg.Reason = "no venue meets the liquidity floor; funds stay idle"
		case errors.Is(err, ErrNoRoutableVenue):
			leg.Reason = "no venue for this asset can be transacted by the executor; funds stay idle"
		case err != nil:
			s.Log.Warn("routable venue lookup", "asset", weight.Asset, "err", err)
			leg.Reason = "market data unavailable"
		default:
			leg.Venue = &v
			leg.Reason = "highest net APY above the TVL floor"
			if note != "" {
				leg.Reason = note
			}
			blended += v.APY * float64(weight.WeightBps) / float64(store.TotalBps)
		}
		legs = append(legs, leg)
	}
	return legs, blended
}

type LegResult struct {
	Asset       string          `json:"asset"`
	AmountUSD   float64         `json:"amount_usd"`
	FromVenueID string          `json:"from_venue_id,omitempty"`
	VenueID     string          `json:"venue_id,omitempty"`
	Project     string          `json:"project,omitempty"`
	APY         float64         `json:"apy,omitempty"`
	ExecutionID string          `json:"execution_id,omitempty"`
	TxHash      string          `json:"tx_hash,omitempty"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason,omitempty"`
	Steps       []executor.Step `json:"steps,omitempty"`
}

func (s *Server) canExecute(w http.ResponseWriter, u store.User) bool {
	var missing string
	switch {
	case !u.Delegated:
		missing = "wallet delegation is missing; grant it in Privy, then POST /v1/me again with the wallet ID Privy returns"
	case u.PrivyWalletID == "":
		missing = "wallet delegation is granted but we have not been told the Privy wallet ID yet; POST /v1/me again to send it"
	default:
		return true
	}
	writeErr(w, http.StatusPreconditionFailed, "cannot execute: "+missing)
	return false
}

const (
	legSubmitted = "submitted"
	legPending   = "pending"
	legFailed    = "failed"
	legSkipped   = "skipped"
)

// ponytail: sentinel venue ID, not a column; add a real `state` column if
// positions grow more lifecycle states than "placed" and "idle".
const IdleVenueID = "idle:wallet"

func legOutcome(resp executor.RouteResponse, err error) (dbStatus, legStatus, reason string) {
	switch {
	case err != nil:
		return "failed", legFailed, err.Error()
	case resp.Status == "pending":
		return "pending", legPending, "submitted but not yet confirmed; receipt poll timed out"
	case resp.Status == "failed":
		if resp.Error == "" {
			return "failed", legFailed, "executor reported failure"
		}
		return "failed", legFailed, resp.Error
	case resp.Status == "confirmed":
		return "confirmed", legSubmitted, ""
	default:
		return "submitted", legSubmitted, ""
	}
}

func fundsUnplaced(resp executor.RouteResponse) bool {
	for _, st := range resp.Steps {
		if st.Outcome == "confirmed" {
			return true
		}
	}
	return false
}

func (s *Server) run(r *http.Request, u store.User, e store.Execution, req executor.RouteRequest) (store.Execution, executor.RouteResponse, LegResult) {
	ctx := r.Context()
	e, err := s.Store.CreateExecution(ctx, e)
	if err != nil {
		s.Log.Error("create execution", "asset", e.Asset, "err", err)
		return e, executor.RouteResponse{}, LegResult{Status: legFailed, Reason: "could not record execution; nothing was submitted"}
	}
	chainID, cerr := chainOf(req.Chain)
	if cerr != nil {
		s.Log.Error("unroutable chain", "chain", req.Chain, "asset", e.Asset, "err", cerr)
		return e, executor.RouteResponse{}, LegResult{
			Status: legFailed,
			Reason: "basket is on an unsupported chain (" + req.Chain + "); nothing was submitted",
		}
	}
	req.ChainID = chainID
	req.ExecutionID = e.ID
	req.UserWallet = u.WalletAddress
	req.PrivyDID = u.PrivyDID
	req.PrivyWalletID = u.PrivyWalletID
	req.MaxSlippageBp = s.MaxSlippageBps

	resp, err := s.Executor.Route(ctx, req)
	dbStatus, legStatus, reason := legOutcome(resp, err)

	var steps json.RawMessage
	if len(resp.Steps) > 0 {
		if raw, merr := json.Marshal(resp.Steps); merr == nil {
			steps = raw
		}
	}
	var txHash, errMsg *string
	if resp.TxHash != "" {
		txHash = &resp.TxHash
	}
	if legStatus == legFailed && reason != "" {
		errMsg = &reason
	}
	if uerr := s.Store.UpdateExecution(ctx, e.ID, dbStatus, txHash, errMsg, steps); uerr != nil {
		s.Log.Error("update execution", "execution_id", e.ID, "status", dbStatus, "err", uerr)
	}
	return e, resp, LegResult{
		ExecutionID: e.ID,
		TxHash:      resp.TxHash,
		Status:      legStatus,
		Reason:      reason,
		Steps:       resp.Steps,
	}
}

func (s *Server) deposit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	var body struct {
		AmountUSD float64 `json:"amount_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AmountUSD <= 0 {
		writeErr(w, http.StatusBadRequest, "amount_usd must be greater than 0")
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

	legs, _ := s.planLegs(r, b, body.AmountUSD)
	results := make([]LegResult, 0, len(legs))
	var submittedUSD float64
	failed, pending := 0, 0

	for _, leg := range legs {
		res := LegResult{Asset: leg.Asset, AmountUSD: leg.AmountUSD}
		if leg.Venue == nil || leg.AmountUSD <= 0 {
			res.Status, res.Reason = legSkipped, leg.Reason
			if leg.AmountUSD <= 0 {
				res.Reason = "leg rounds to zero"
			}
			results = append(results, res)
			continue
		}
		res.VenueID, res.Project, res.APY = leg.Venue.ID, leg.Venue.Project, leg.Venue.APY

		basketID := b.ID
		_, _, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &basketID,
			Kind:      "deposit",
			Asset:     leg.Asset,
			ToVenue:   &leg.Venue.ID,
			AmountUSD: leg.AmountUSD,
		}, executor.RouteRequest{
			Chain:     b.Chain,
			Asset:     leg.Asset,
			AmountUSD: leg.AmountUSD,
			Action:    "deposit",
			ToVenueID: leg.Venue.ID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("deposit leg failed", "asset", leg.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			results = append(results, res)
			continue
		case legPending:
			pending++
			s.Log.Warn("deposit leg pending", "asset", leg.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		submittedUSD += leg.AmountUSD

		if err := s.Store.UpsertPosition(r.Context(), store.Position{
			UserID:    u.ID,
			BasketID:  b.ID,
			Asset:     leg.Asset,
			VenueID:   leg.Venue.ID,
			Chain:     leg.Venue.Chain,
			Project:   leg.Venue.Project,
			AmountUSD: leg.AmountUSD,
			EntryAPY:  leg.Venue.APY,
		}); err != nil {
			s.Log.Error("upsert position", "asset", leg.Asset, "err", err)
			res.Reason = "submitted, but the position record failed to save"
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     b.ID,
		"amount_usd":    body.AmountUSD,
		"submitted_usd": submittedUSD,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

func partitionWithdrawable(positions []store.Position) (withdrawable []store.Position, skipped []LegResult, available float64) {
	withdrawable = make([]store.Position, 0, len(positions))
	skipped = make([]LegResult, 0)
	for _, p := range positions {
		if p.VenueID == IdleVenueID {
			skipped = append(skipped, LegResult{
				Asset: p.Asset, FromVenueID: p.VenueID, Status: legSkipped,
				Reason: "already idle in your wallet; nothing to withdraw from a venue",
			})
			continue
		}
		withdrawable = append(withdrawable, p)
		available += p.AmountUSD
	}
	return withdrawable, skipped, available
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	var body struct {
		AmountUSD float64 `json:"amount_usd"`
		All       bool    `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !body.All && body.AmountUSD <= 0 {
		writeErr(w, http.StatusBadRequest, "send amount_usd greater than 0, or all: true")
		return
	}

	basketID := r.PathValue("id")
	positions, err := s.Store.Positions(r.Context(), u.ID, basketID)
	if err != nil {
		s.Log.Error("positions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	withdrawable, skipped, available := partitionWithdrawable(positions)

	amount := body.AmountUSD
	if body.All {
		amount = available
	}
	if amount > available+0.005 {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"amount_usd %.2f exceeds the %.2f held in this basket", amount, available))
		return
	}
	if len(withdrawable) == 0 || amount <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"basket_id": basketID, "amount_usd": 0, "withdrawn_usd": float64(0),
			"failed_legs": 0, "pending_legs": 0, "legs": skipped,
		})
		return
	}

	values := make([]float64, len(withdrawable))
	for i, p := range withdrawable {
		values[i] = p.AmountUSD
	}
	amounts := splitProportional(amount, values)

	results := make([]LegResult, 0, len(positions))
	results = append(results, skipped...)
	var withdrawnUSD float64
	failed, pending := 0, 0

	for i, p := range withdrawable {
		legUSD := amounts[i]
		res := LegResult{Asset: p.Asset, AmountUSD: legUSD, FromVenueID: p.VenueID, Project: p.Project}
		if legUSD <= 0 {
			res.Status, res.Reason = legSkipped, "leg rounds to zero"
			results = append(results, res)
			continue
		}

		bid := p.BasketID
		_, _, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &bid,
			Kind:      "withdraw",
			Asset:     p.Asset,
			FromVenue: &p.VenueID,
			AmountUSD: legUSD,
		}, executor.RouteRequest{
			Chain:       p.Chain,
			Asset:       p.Asset,
			AmountUSD:   legUSD,
			Action:      "withdraw",
			FromVenueID: p.VenueID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("withdraw leg failed", "asset", p.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			results = append(results, res)
			continue
		case legPending:
			pending++
			s.Log.Warn("withdraw leg pending", "asset", p.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		withdrawnUSD += legUSD

		remaining := math.Round((p.AmountUSD-legUSD)*100) / 100
		if remaining <= 0 {
			if err := s.Store.DeletePosition(r.Context(), p.ID); err != nil {
				s.Log.Error("delete position", "asset", p.Asset, "err", err)
				res.Reason = "withdrawn, but the position record failed to clear"
			}
		} else {
			p.AmountUSD = remaining
			if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
				s.Log.Error("upsert position", "asset", p.Asset, "err", err)
				res.Reason = "withdrawn, but the position record failed to save"
			}
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     basketID,
		"amount_usd":    amount,
		"withdrawn_usd": withdrawnUSD,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

func (s *Server) rebalance(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	basketID := r.PathValue("id")
	actor := "user"
	if c, ok := auth.FromContext(r.Context()); ok && c.ViaKeeper {
		actor = "keeper"
	}
	s.Log.Info("rebalance requested", "actor", actor, "user_id", u.ID, "basket_id", basketID)

	positions, err := s.Store.Positions(r.Context(), u.ID, basketID)
	if err != nil {
		s.Log.Error("positions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	results := make([]LegResult, 0, len(positions))
	failed, pending, moved := 0, 0, 0
	for _, p := range positions {
		res := LegResult{Asset: p.Asset, AmountUSD: p.AmountUSD, FromVenueID: p.VenueID}
		best, note, err := s.bestRoutable(r.Context(), p.Asset, p.Chain)
		if err != nil {
			res.Status, res.Reason = legSkipped, "no better executable venue available"
			if errors.Is(err, ErrNoRoutableVenue) {
				res.Reason = "no venue for this asset can be transacted by the executor"
			}
			s.Log.Info("skip rebalance leg", "asset", p.Asset, "chain", p.Chain, "err", err)
			results = append(results, res)
			continue
		}
		_, drift := driftAPY(p, best)
		if !shouldRebalance(drift, s.RebalanceThresholdAPY) {
			res.Status = legSkipped
			res.Reason = "drift below threshold"
			res.VenueID, res.APY = best.ID, best.APY
			results = append(results, res)
			continue
		}
		res.VenueID, res.Project, res.APY = best.ID, best.Project, best.APY
		if note != "" {
			res.Reason = note
		}

		action, fromVenue := "rebalance", p.VenueID
		if p.VenueID == IdleVenueID {
			action, fromVenue = "deposit", ""
		}

		bid := p.BasketID
		_, resp, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &bid,
			Kind:      "rebalance",
			Asset:     p.Asset,
			FromVenue: &p.VenueID,
			ToVenue:   &best.ID,
			AmountUSD: p.AmountUSD,
		}, executor.RouteRequest{
			Chain:       p.Chain,
			Asset:       p.Asset,
			AmountUSD:   p.AmountUSD,
			Action:      action,
			FromVenueID: fromVenue,
			ToVenueID:   best.ID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("rebalance leg failed", "actor", actor, "asset", p.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			if fundsUnplaced(resp) {
				res.VenueID, res.Reason = IdleVenueID,
					"withdrew but failed to redeposit; funds are idle in your wallet"
				p.VenueID, p.Project, p.EntryAPY = IdleVenueID, "wallet", 0
				if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
					s.Log.Error("mark position idle", "asset", p.Asset, "err", err)
				}
			}
			results = append(results, res)
			continue
		case legPending:
			pending++
			s.Log.Warn("rebalance leg pending", "asset", p.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		moved++

		p.VenueID, p.Chain, p.Project, p.EntryAPY = best.ID, best.Chain, best.Project, best.APY
		if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
			s.Log.Error("upsert position", "asset", p.Asset, "err", err)
			res.Reason = "submitted, but the position record failed to save"
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     basketID,
		"threshold_apy": s.RebalanceThresholdAPY,
		"moved_legs":    moved,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

func settledStatus(failed, pending int) int {
	if failed > 0 || pending > 0 {
		return http.StatusMultiStatus
	}
	return http.StatusOK
}

func shouldRebalance(drift, threshold float64) bool { return drift > threshold }
